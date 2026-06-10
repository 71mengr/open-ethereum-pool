package proxy

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	//"sync"
	"time"

	"github.com/sammy007/open-ethereum-pool/util"
)

const MaxReqSize = 1024

// formatRandomXTarget converts difficulty to 64-character hex target for RandomX
func formatRandomXTarget(diff int64) string {
	if diff <= 0 {
		diff = 1000
	}
	
	// 2^256 - 1
	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	
	// target = maxUint256 / difficulty
	target := new(big.Int).Div(maxUint256, big.NewInt(diff))
	
	// Format as 64-character hex (32 bytes) without 0x prefix
	hexStr := fmt.Sprintf("%064x", target)
	
	// Ensure length is exactly 64
	if len(hexStr) != 64 {
		hexStr = fmt.Sprintf("%064s", hexStr)
	}
	
	return "0x" + hexStr
}

// remove0x removes 0x prefix if present
func remove0x(s string) string {
	return strings.TrimPrefix(s, "0x")
}

// add0x adds 0x prefix if not present
func add0x(s string) string {
	if !strings.HasPrefix(s, "0x") && len(s) > 0 {
		return "0x" + s
	}
	return s
}

// littleEndianNonce converts a hex nonce string (big-endian from XMRig) to little-endian bytes
func littleEndianNonce(nonceHex string) ([]byte, error) {
	nonceHex = remove0x(nonceHex)
	
	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil {
		return nil, err
	}
	
	// Ensure 8 bytes
	if len(nonceBytes) != 8 {
		padded := make([]byte, 8)
		if len(nonceBytes) < 8 {
			copy(padded[8-len(nonceBytes):], nonceBytes)
		} else {
			copy(padded[:], nonceBytes[:8])
		}
		nonceBytes = padded
	}
	
	// Convert from big-endian to little-endian
	nonceLE := make([]byte, 8)
	nonceLE[0] = nonceBytes[7]
	nonceLE[1] = nonceBytes[6]
	nonceLE[2] = nonceBytes[5]
	nonceLE[3] = nonceBytes[4]
	nonceLE[4] = nonceBytes[3]
	nonceLE[5] = nonceBytes[2]
	nonceLE[6] = nonceBytes[1]
	nonceLE[7] = nonceBytes[0]
	
	return nonceLE, nil
}

// getWorkFromDaemon calls go-ethereum's eth_getWork RPC
func (s *ProxyServer) getWorkFromDaemon() ([]string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	
	rpcReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_getWork",
		"params":  []interface{}{},
		"id":      1,
	}
	
	reqBody, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, err
	}
	
	var daemonUrl string
	if len(s.config.Upstream) > 0 && s.config.Upstream[0].Url != "" {
		daemonUrl = s.config.Upstream[0].Url
	} else {
		daemonUrl = "http://localhost:8545"
	}
	
	resp, err := client.Post(daemonUrl, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("daemon connection failed: %v", err)
	}
	defer resp.Body.Close()
	
	var result struct {
		Result []string `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode daemon response: %v", err)
	}
	
	if result.Error != nil {
		return nil, fmt.Errorf("daemon error: %s", result.Error.Message)
	}
	
	if len(result.Result) < 3 {
		return nil, fmt.Errorf("invalid work response from daemon")
	}
	
	return result.Result, nil
}

// updateBlockTemplate fetches the latest work from daemon and updates the template
func (s *ProxyServer) updateBlockTemplate() {
	work, err := s.getWorkFromDaemon()
	if err != nil {
		log.Printf("⚠️ Failed to get work from daemon: %v", err)
		return
	}
	
	if len(work) < 4 {
		log.Printf("⚠️ Invalid work response from daemon: %+v", work)
		return
	}
	
	// work[0] = seal hash (what miners hash)
	// work[1] = seed hash (for RandomX)
	// work[2] = target
	// work[3] = block number (hex)
	
	height, err := strconv.ParseUint(remove0x(work[3]), 16, 64)
	if err != nil {
		log.Printf("⚠️ Failed to parse height: %v", err)
		return
	}
	
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	
	if s.currentTemplate == nil {
		s.currentTemplate = &BlockTemplate{}
	}
	
	s.currentTemplate.Height = height
	s.currentTemplate.SealHash = remove0x(work[0])
	s.currentTemplate.SeedHash = remove0x(work[1])
	s.currentTemplate.Target = work[2]
	s.currentTemplate.Difficulty = util.TargetHexToDiff(work[2])
	
	log.Printf("�� Updated block template - height=%d, seal=%s..., seed=%s...",
		height, s.currentTemplate.SealHash[:16], s.currentTemplate.SeedHash[:16])
}

// currentBlockTemplate returns the current mining template
func (s *ProxyServer) currentBlockTemplate() *BlockTemplate {
	s.templateMu.RLock()
	defer s.templateMu.RUnlock()
	return s.currentTemplate
}

// startTemplateUpdater periodically fetches new templates from daemon
func (s *ProxyServer) startTemplateUpdater() {
	// Update immediately
	s.updateBlockTemplate()
	
	// Then update every 2 seconds
	ticker := time.NewTicker(2 * time.Second)
	go func() {
		for range ticker.C {
			s.updateBlockTemplate()
		}
	}()
}

func (s *ProxyServer) ListenTCP() {
	timeout := util.MustParseDuration(s.config.Proxy.Stratum.Timeout)
	s.timeout = timeout

	addr, err := net.ResolveTCPAddr("tcp", s.config.Proxy.Stratum.Listen)
	if err != nil {
		log.Fatalf("Stratum resolve error: %v", err)
	}

	server, err := net.ListenTCP("tcp", addr)
	if err != nil {
		log.Fatalf("Stratum listen error: %v", err)
	}
	defer server.Close()

	log.Printf("✅ Stratum server listening on %s", s.config.Proxy.Stratum.Listen)

	// Start template updater
	s.startTemplateUpdater()

	accept := make(chan int, s.config.Proxy.Stratum.MaxConn)

	for {
		conn, err := server.AcceptTCP()
		if err != nil {
			continue
		}
		conn.SetKeepAlive(true)

		ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		if s.policy.IsBanned(ip) || !s.policy.ApplyLimitPolicy(ip) {
			conn.Close()
			continue
		}

		cs := &Session{conn: conn, ip: ip}
		accept <- 1
		go func(cs *Session) {
			err := s.handleTCPClient(cs)
			if err != nil {
				s.removeSession(cs)
				conn.Close()
			}
			<-accept
		}(cs)
	}
}

func (s *ProxyServer) handleTCPClient(cs *Session) error {
	cs.enc = json.NewEncoder(cs.conn)
	reader := bufio.NewReaderSize(cs.conn, MaxReqSize)
	s.setDeadline(cs.conn)

	for {
		data, isPrefix, err := reader.ReadLine()
		if isPrefix {
			log.Printf("Socket flood from %s", cs.ip)
			s.policy.BanClient(cs.ip)
			return err
		}
		if err == io.EOF {
			log.Printf("Client %s disconnected", cs.ip)
			return nil
		}
		if err != nil {
			return err
		}

		if len(data) > 1 {
			var req StratumReq
			if err := json.Unmarshal(data, &req); err != nil {
				s.policy.ApplyMalformedPolicy(cs.ip)
				return err
			}

			s.setDeadline(cs.conn)
			if err := cs.handleTCPMessage(s, &req); err != nil {
				return err
			}
		}
	}
}

func (cs *Session) handleTCPMessage(s *ProxyServer, req *StratumReq) error {
	switch req.Method {
	case "eth_submitLogin":
		var params []string
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Invalid params"})
		}

		if len(params) == 0 {
			return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Login required"})
		}

		login := strings.ToLower(params[0])
		if !util.IsValidHexAddress(login) {
			return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Invalid address"})
		}

		if !s.policy.ApplyLoginPolicy(login, cs.ip) {
			return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "You are blacklisted"})
		}

		cs.login = login
		s.registerSession(cs)
		log.Printf("✅ XMRig (TKM) logged in: %s@%s", login[:16], cs.ip)

		// Make sure we have a template
		s.updateBlockTemplate()
		
		t := s.currentBlockTemplate()
		if t != nil && len(t.SealHash) > 0 {
			poolDiff := s.GetPoolShareDifficulty()
			if poolDiff <= 0 {
				poolDiff = 1000
			}
			target := formatRandomXTarget(poolDiff)
			
			// For TKM, the blob is the seal hash (what miners hash)
			jobResult := map[string]interface{}{
				"blob":      add0x(t.SealHash),
				"job_id":    fmt.Sprintf("%d", t.Height),
				"target":    target,
				"seed_hash": add0x(t.SeedHash),
				"height":    t.Height,
			}
			
			log.Printf("�� Initial job to %s: height=%d, seal=%s..., target=%s...", 
				cs.ip, t.Height, t.SealHash[:16], target[2:18])
			
			return cs.sendTCPResult(req.Id, jobResult)
		}
		
		log.Printf("⚠️ No block template available for initial job")
		return cs.sendTCPResult(req.Id, true)

	case "eth_getWork":
		// Update template from daemon first
		s.updateBlockTemplate()
		
		t := s.currentBlockTemplate()
		if t == nil || len(t.SealHash) == 0 {
			log.Printf("⚠️ Work not ready - no template available")
			return cs.sendTCPError(req.Id, &ErrorReply{Code: 0, Message: "Work not ready"})
		}

		poolDiff := s.GetPoolShareDifficulty()
		if poolDiff <= 0 {
			poolDiff = 1000
		}
		target := formatRandomXTarget(poolDiff)
		
		// Response format: [seal_hash, seed_hash, target]
		response := []string{
			add0x(t.SealHash),
			add0x(t.SeedHash),
			target,
		}
		
		log.Printf("�� eth_getWork to %s: seal=%s..., seed=%s..., target=%s... (diff=%d)",
			cs.ip, 
			response[0][2:18], 
			response[1][2:18], 
			target[2:18],
			poolDiff)
		
		return cs.sendTCPResult(req.Id, response)

	case "eth_submitWork":
		var params []string
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Invalid params"})
		}

		if len(params) < 3 {
			return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Invalid params"})
		}

		nonceHex := params[0]
		sealHashHex := params[1]
		mixDigestHex := params[2]

		log.Printf("⛏️ eth_submitWork from %s: nonce=%s, seal=%s..., mix=%s...",
			cs.ip, nonceHex, sealHashHex[:16], mixDigestHex[:16])

		t := s.currentBlockTemplate()
		if t == nil {
			log.Printf("⚠️ No template available for share submission")
			return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "No template"})
		}

		// Verify seal hash matches current work
		currentSealHash := remove0x(t.SealHash)
		submittedSealHash := remove0x(sealHashHex)
		
		if submittedSealHash != currentSealHash {
			log.Printf("⚠️ Seal hash mismatch: expected %s..., got %s...", 
				currentSealHash[:16], submittedSealHash[:16])
			return cs.sendTCPResult(req.Id, false)
		}
		
		log.Printf("✅ Seal hash matches for %s", cs.ip)

		// Process the share
		exist, valid := s.processTKMShare(cs.login, "0", cs.ip, t, nonceHex, sealHashHex, mixDigestHex)

		if exist {
			log.Printf("⚠️ Duplicate share from %s", cs.ip)
			return cs.sendTCPError(req.Id, &ErrorReply{Code: 22, Message: "Duplicate share"})
		}
		
		if valid {
			log.Printf("✅ Valid share from %s! nonce=%s", cs.ip, nonceHex)
		} else {
			log.Printf("❌ Invalid share from %s", cs.ip)
		}

		return cs.sendTCPResult(req.Id, valid)

	case "eth_submitHashrate":
		return cs.sendTCPResult(req.Id, true)

	case "keepalive":
		log.Printf("�� Keepalive from %s", cs.ip)
		return cs.sendTCPResult(req.Id, "OK")

	default:
		log.Printf("❓ Unknown method from %s: %s", cs.ip, req.Method)
		return cs.sendTCPError(req.Id, &ErrorReply{Code: -3, Message: "Method not found"})
	}
}

// processTKMShare handles TKM RandomX share verification and records accepted work.
func (s *ProxyServer) processTKMShare(login, id, ip string, t *BlockTemplate, nonceHex, sealHashHex, mixDigestHex string) (bool, bool) {
	// Remove 0x prefixes
	sealHashHex = remove0x(sealHashHex)
	mixDigestHex = remove0x(mixDigestHex)
	
	// Decode seal hash
	sealHash, err := hex.DecodeString(sealHashHex)
	if err != nil || len(sealHash) != 32 {
		log.Printf("Invalid seal hash: %v", err)
		return false, false
	}
	
	// Convert nonce from big-endian (XMRig) to little-endian (RandomX)
	_, err = littleEndianNonce(nonceHex)
	if err != nil {
		log.Printf("Invalid nonce: %v", err)
		return false, false
	}
	
	// Decode submitted mix digest
	submittedMix, err := hex.DecodeString(mixDigestHex)
	if err != nil || len(submittedMix) != 32 {
		log.Printf("Invalid mix digest: %v", err)
		return false, false
	}
	
	// Check difficulty
	hashBig := new(big.Int).SetBytes(submittedMix)
	poolDiff := s.GetPoolShareDifficulty()
	if poolDiff <= 0 {
		poolDiff = 1000
	}
	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	targetBig := new(big.Int).Div(maxUint256, big.NewInt(poolDiff))
	
	if hashBig.Cmp(targetBig) > 0 {
		log.Printf("⚠️ Hash difficulty too low: %s > %s", 
			hashBig.String(), targetBig.String())
		return false, false
	}
	
	shareDiff := big.NewInt(0)
	if hashBig.Sign() > 0 {
		shareDiff.Div(maxUint256, hashBig)
	}
	if shareDiff.Sign() == 0 {
		shareDiff.SetInt64(poolDiff)
	}

	log.Printf("✅ Share accepted - nonce=%s, hash=%x..., diff=%s",
		nonceHex, submittedMix[:8], shareDiff.String())

	params := []string{add0x(nonceHex), add0x(sealHashHex), add0x(mixDigestHex)}
	isBlock := false
	if t.Target != "" {
		targetBytes, err := hex.DecodeString(remove0x(t.Target))
		if err == nil && len(targetBytes) > 0 {
			isBlock = hashBig.Cmp(new(big.Int).SetBytes(targetBytes)) <= 0
		}
	}
	if !isBlock && t.Difficulty != nil && t.Difficulty.Sign() > 0 {
		isBlock = shareDiff.Cmp(t.Difficulty) >= 0
	}

	roundDiff := poolDiff
	if t.Difficulty != nil && t.Difficulty.Sign() > 0 {
		roundDiff = t.Difficulty.Int64()
	}

	if isBlock {
		log.Printf("�� Block candidate detected! Submitting to daemon...")
		ok, err := s.rpc().SubmitBlock(params)
		if err != nil {
			log.Printf("Block submission error: %v", err)
		} else if !ok {
			log.Printf("Block rejected by daemon, accepting as share")
		} else {
			log.Printf("✅ BLOCK FOUND AND ACCEPTED! Height: %d", t.Height)
			go s.fetchBlockTemplate()
			exist, err := s.backend.WriteBlock(login, id, params, shareDiff.Int64(), roundDiff, t.Height, s.hashrateExpiration)
			if err != nil {
				log.Printf("Failed to insert block candidate into backend: %v", err)
				return false, false
			}
			return exist, true
		}
	}

	exist, err := s.backend.WriteShare(login, id, params, shareDiff.Int64(), t.Height, s.hashrateExpiration)
	if err != nil {
		log.Printf("Failed to insert share data into backend: %v", err)
		return false, false
	}
	return exist, true
}

// getSeedHashFromDaemon gets the seed hash for a specific block height
func (s *ProxyServer) getSeedHashFromDaemon(height uint64) string {
	client := &http.Client{Timeout: 5 * time.Second}
	
	heightHex := fmt.Sprintf("0x%x", height)
	
	rpcReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "randomx_getSeedHash",
		"params":  []interface{}{heightHex},
		"id":      1,
	}
	
	reqBody, err := json.Marshal(rpcReq)
	if err == nil {
		var daemonUrl string
		if len(s.config.Upstream) > 0 && s.config.Upstream[0].Url != "" {
			daemonUrl = s.config.Upstream[0].Url
		} else {
			daemonUrl = "http://localhost:8545"
		}
		
		resp, err := client.Post(daemonUrl, "application/json", bytes.NewReader(reqBody))
		if err == nil {
			defer resp.Body.Close()
			
			var result struct {
				Result string `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			
			if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
				if result.Error == nil && result.Result != "" {
					if !strings.HasPrefix(result.Result, "0x") {
						result.Result = "0x" + result.Result
					}
					return result.Result
				}
			}
		}
	}
	
	// For epoch 0, seed hash is all zeros
	if height/2048 == 0 {
		return "0x0000000000000000000000000000000000000000000000000000000000000000"
	}
	
	return "0x0000000000000000000000000000000000000000000000000000000000000000"
}

func (cs *Session) sendTCPResult(id json.RawMessage, result interface{}) error {
	cs.Lock()
	defer cs.Unlock()
	return cs.enc.Encode(JSONRpcResp{Id: id, Version: "2.0", Result: result})
}

func (cs *Session) sendTCPError(id json.RawMessage, reply *ErrorReply) error {
	cs.Lock()
	defer cs.Unlock()
	err := cs.enc.Encode(JSONRpcResp{Id: id, Version: "2.0", Error: reply})
	if err != nil {
		return err
	}
	return errors.New(reply.Message)
}

// pushNewJob sends a job notification to the miner
func (cs *Session) pushNewJob(job map[string]interface{}) error {
	cs.Lock()
	defer cs.Unlock()

	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "job",
		"params": []interface{}{
			job["job_id"],
			job["blob"],
			job["target"],
			job["seed_hash"],
			job["height"],
		},
		"id": 0,
	}

	return cs.enc.Encode(msg)
}

func (s *ProxyServer) broadcastNewJobs() {
	t := s.currentBlockTemplate()
	if t == nil || len(t.SealHash) == 0 || s.isSick() {
		return
	}

	poolDiff := s.GetPoolShareDifficulty()
	if poolDiff <= 0 {
		poolDiff = 1000
	}
	
	target := formatRandomXTarget(poolDiff)

	job := map[string]interface{}{
		"blob":      add0x(t.SealHash),
		"job_id":    fmt.Sprintf("%d", t.Height),
		"target":    target,
		"seed_hash": add0x(t.SeedHash),
		"height":    t.Height,
	}

	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()

	count := len(s.sessions)
	if count == 0 {
		return
	}

	log.Printf("�� Broadcasting new job to %d miners - height=%d, diff=%d, seal=%s..., target=%s...",
		count, t.Height, poolDiff, t.SealHash[:16], target[2:18])

	for session := range s.sessions {
		go func(cs *Session) {
			if err := cs.pushNewJob(job); err != nil {
				log.Printf("Failed to push job to %s: %v", cs.ip, err)
				s.removeSession(cs)
			} else {
				s.setDeadline(cs.conn)
			}
		}(session)
	}
}

func (s *ProxyServer) setDeadline(conn *net.TCPConn) {
	conn.SetDeadline(time.Now().Add(s.timeout))
}

func (s *ProxyServer) registerSession(cs *Session) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	s.sessions[cs] = struct{}{}
}

func (s *ProxyServer) removeSession(cs *Session) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	delete(s.sessions, cs)
}

func (s *ProxyServer) GetPoolShareDifficulty() int64 {
	if s.config.Proxy.RandomX.Enabled {
		if s.config.Proxy.RandomX.ShareDifficulty > 0 {
			return s.config.Proxy.RandomX.ShareDifficulty
		}
		return DefaultRandomXShareDifficulty
	}
	if s.config.Proxy.Difficulty > 0 {
		return s.config.Proxy.Difficulty
	}
	return 1 // Start with minimum difficulty for testing
}

// Types
type StratumReq struct {
	Id     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Worker string          `json:"worker"`
}

type JSONRpcResp struct {
	Id      json.RawMessage `json:"id"`
	Version string          `json:"jsonrpc"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *ErrorReply     `json:"error,omitempty"`
}

type ErrorReply struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
