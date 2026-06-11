package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
//	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
//	"sync"
	"time"

	"github.com/sammy007/open-ethereum-pool/util"
)

const (
	MaxReqSize                       = 1024
	DefaultRandomXShareDifficulty    = 1000
	ConnectionTimeout                = 300 * time.Second
	TemplateUpdateInterval           = 1 * time.Second
	MaxStoredTemplates               = 8
)

// formatRandomXTarget converts difficulty to 64-character hex target for RandomX
/*func formatRandomXTarget(diff int64) string {
	if diff <= 0 {
		diff = DefaultRandomXShareDifficulty
	}
	
	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	target := new(big.Int).Div(maxUint256, big.NewInt(diff))
	hexStr := fmt.Sprintf("%064x", target)
	
	if len(hexStr) != 64 {
		hexStr = fmt.Sprintf("%064s", hexStr)
	}
	
	return hexStr
}
*/
// remove0x removes 0x prefix if present
func remove0x(s string) string {
	return strings.TrimPrefix(s, "0x")
}

// add0x adds 0x prefix if not present
/*func add0x(s string) string {
	if !strings.HasPrefix(s, "0x") && len(s) > 0 {
		return "0x" + s
	}
	return s
}

// shortHex returns a short hex string for logging
func shortHex(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
*/
// getWorkFromDaemon calls go-ethereum's eth_getWork RPC
func (s *ProxyServer) getWorkFromDaemon() ([]string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

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

	if len(result.Result) < 4 {
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

	height, err := strconv.ParseUint(remove0x(work[3]), 16, 64)
	if err != nil {
		log.Printf("⚠️ Failed to parse height: %v", err)
		return
	}

	sealHash := remove0x(work[0])
	seedHash := remove0x(work[1])
	
	// Check if seal hash changed
	oldSealHash := ""
	if s.currentTemplate != nil {
		oldSealHash = s.currentTemplate.SealHash
	}
	
	newTemplate := &BlockTemplate{
		Header:     add0x(sealHash),
		Seed:       add0x(seedHash),
		SealHash:   sealHash,
		SeedHash:   seedHash,
		Target:     work[2],
		Difficulty: util.TargetHexToDiff(work[2]),
		Height:     height,
	}

	s.templateMu.Lock()
	s.currentTemplate = newTemplate
	
	if s.sealTemplates == nil {
		s.sealTemplates = make(map[string]*BlockTemplate)
	}
	if _, ok := s.sealTemplates[sealHash]; !ok {
		s.sealOrder = append(s.sealOrder, sealHash)
	}
	s.sealTemplates[sealHash] = newTemplate
	
	// Keep only last MaxStoredTemplates templates
	for len(s.sealOrder) > MaxStoredTemplates {
		oldest := s.sealOrder[0]
		s.sealOrder = s.sealOrder[1:]
		delete(s.sealTemplates, oldest)
	}
	s.templateMu.Unlock()

	log.Printf("✅ Updated block template - height=%d, seal=%s..., seed=%s...",
		height, shortHex(sealHash, 16), shortHex(seedHash, 16))
	
	// Broadcast new job to all miners if seal hash changed
	if oldSealHash != "" && oldSealHash != sealHash {
		log.Printf("�� New block found! Broadcasting new job to miners...")
		go s.broadcastNewJobs()
	}
}

// currentBlockTemplate returns the current mining template
func (s *ProxyServer) currentBlockTemplate() *BlockTemplate {
	s.templateMu.RLock()
	defer s.templateMu.RUnlock()
	
	if s.currentTemplate != nil {
		return s.currentTemplate
	}
	
	if t := s.blockTemplate.Load(); t != nil {
		template := t.(*BlockTemplate)
		if template.SealHash == "" {
			template.SealHash = remove0x(template.Header)
		}
		if template.SeedHash == "" {
			template.SeedHash = remove0x(template.Seed)
		}
		return template
	}
	return nil
}

// blockTemplateForSeal returns template for a specific seal hash
func (s *ProxyServer) blockTemplateForSeal(sealHash string) *BlockTemplate {
	sealHash = remove0x(sealHash)
	s.templateMu.RLock()
	defer s.templateMu.RUnlock()
	
	if s.currentTemplate != nil && remove0x(s.currentTemplate.SealHash) == sealHash {
		return s.currentTemplate
	}
	if s.sealTemplates != nil {
		if template := s.sealTemplates[sealHash]; template != nil {
			return template
		}
	}
	return nil
}

// startTemplateUpdater periodically fetches new templates from daemon
func (s *ProxyServer) startTemplateUpdater() {
	s.updateBlockTemplate()
	ticker := time.NewTicker(TemplateUpdateInterval)
	go func() {
		for range ticker.C {
			s.updateBlockTemplate()
		}
	}()
}

// ListenTCP starts the TCP listener for stratum connections
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

	s.startTemplateUpdater()

	accept := make(chan int, s.config.Proxy.Stratum.MaxConn)

	for {
		conn, err := server.AcceptTCP()
		if err != nil {
			log.Printf("Accept error: %v", err)
			continue
		}
		
		conn.SetKeepAlive(true)
		conn.SetKeepAlivePeriod(60 * time.Second)

		ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		if s.policy.IsBanned(ip) || !s.policy.ApplyLimitPolicy(ip) {
			log.Printf("Connection rejected from banned IP: %s", ip)
			conn.Close()
			continue
		}

		cs := &Session{conn: conn, ip: ip}
		accept <- 1
		
		go func(cs *Session) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Recovered from panic in client handler: %v", r)
				}
				<-accept
			}()
			
			if err := s.handleTCPClient(cs); err != nil {
				log.Printf("Client handler error for %s: %v", cs.ip, err)
			}
			s.removeSession(cs)
			conn.Close()
		}(cs)
	}
}

// handleTCPClient handles a single TCP client connection
func (s *ProxyServer) handleTCPClient(cs *Session) error {
	cs.enc = json.NewEncoder(cs.conn)
	reader := bufio.NewReaderSize(cs.conn, MaxReqSize)
	s.setDeadline(cs.conn)

	for {
		data, isPrefix, err := reader.ReadLine()
		if err != nil {
			if err == io.EOF {
				log.Printf("Client %s disconnected", cs.ip)
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				log.Printf("Client %s timeout", cs.ip)
				return nil
			}
			log.Printf("Read error from %s: %v", cs.ip, err)
			return err
		}
		
		if isPrefix {
			log.Printf("Line too long from %s", cs.ip)
			s.policy.BanClient(cs.ip)
			return errors.New("line too long")
		}

		if len(data) > 1 {
			var req StratumReq
			if err := json.Unmarshal(data, &req); err != nil {
				log.Printf("JSON decode error from %s: %v", cs.ip, err)
				s.policy.ApplyMalformedPolicy(cs.ip)
				continue
			}

			s.setDeadline(cs.conn)
			
			if err := cs.handleTCPMessage(s, &req); err != nil {
				log.Printf("Message handler error for %s: %v", cs.ip, err)
				continue
			}
		}
	}
}

// handleTCPMessage handles individual stratum messages
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

		// Send initial job
		s.updateBlockTemplate()
		t := s.currentBlockTemplate()
		
		if t != nil && len(t.SealHash) > 0 {
			poolDiff := s.GetPoolShareDifficulty()
			if poolDiff <= 0 {
				poolDiff = DefaultRandomXShareDifficulty
			}
			target := formatRandomXTarget(poolDiff)

			jobResult := map[string]interface{}{
				"blob":      add0x(t.SealHash),
				"job_id":    fmt.Sprintf("%d", t.Height),
				"target":    target,
				"seed_hash": add0x(t.SeedHash),
				"height":    t.Height,
			}

			log.Printf("�� Initial job to %s: height=%d, seal=%s...",
				cs.ip, t.Height, shortHex(t.SealHash, 16))

			return cs.sendTCPResult(req.Id, jobResult)
		}

		log.Printf("⚠️ No block template available for %s", cs.ip)
		return cs.sendTCPResult(req.Id, true)

	case "eth_getWork":
		t := s.currentBlockTemplate()
		if t == nil || len(t.SealHash) == 0 {
			return cs.sendTCPError(req.Id, &ErrorReply{Code: 0, Message: "Work not ready"})
		}

		poolDiff := s.GetPoolShareDifficulty()
		if poolDiff <= 0 {
			poolDiff = DefaultRandomXShareDifficulty
		}
		target := formatRandomXTarget(poolDiff)

		response := []string{
			add0x(t.SealHash),
			add0x(t.SeedHash),
			target,
		}

		log.Printf("�� eth_getWork to %s: seal=%s..., target=%s...",
			cs.ip, shortHex(response[0], 18), target[:16])

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

		log.Printf("⛏️ Share from %s: nonce=%s", cs.ip, nonceHex)

		submittedSealHash := remove0x(sealHashHex)
		t := s.blockTemplateForSeal(submittedSealHash)
		
		if t == nil {
			// Try to refresh template and check again
			s.updateBlockTemplate()
			t = s.blockTemplateForSeal(submittedSealHash)
			if t == nil {
				log.Printf("⚠️ Seal hash not found for %s: %s...", cs.ip, shortHex(submittedSealHash, 16))
				// Send current template to miner to resync
				currentT := s.currentBlockTemplate()
				if currentT != nil {
					poolDiff := s.GetPoolShareDifficulty()
					if poolDiff <= 0 {
						poolDiff = DefaultRandomXShareDifficulty
					}
					target := formatRandomXTarget(poolDiff)
					response := []string{
						add0x(currentT.SealHash),
						add0x(currentT.SeedHash),
						target,
					}
					cs.sendTCPResult(req.Id, response)
				}
				return cs.sendTCPResult(req.Id, false)
			}
		}

		// Process share
		exist, valid := s.processTKMShare(cs.login, "", cs.ip, t, nonceHex, sealHashHex, mixDigestHex)

		if exist {
			log.Printf("⚠️ Duplicate share from %s", cs.ip)
			return cs.sendTCPError(req.Id, &ErrorReply{Code: 22, Message: "Duplicate share"})
		}

		if valid {
			log.Printf("✅ Valid share accepted from %s", cs.ip)
		} else {
			log.Printf("❌ Invalid share rejected from %s", cs.ip)
		}
		
		return cs.sendTCPResult(req.Id, valid)

	case "eth_submitHashrate":
		return cs.sendTCPResult(req.Id, true)

	case "keepalive":
		return cs.sendTCPResult(req.Id, "OK")

	default:
		log.Printf("❓ Unknown method from %s: %s", cs.ip, req.Method)
		return cs.sendTCPError(req.Id, &ErrorReply{Code: -3, Message: "Method not found"})
	}
}

// sendTCPResult sends a JSON-RPC result response over TCP
func (cs *Session) sendTCPResult(id json.RawMessage, result interface{}) error {
	cs.Lock()
	defer cs.Unlock()
	
	resp := JSONRpcResp{
		Id:      id,
		Version: "2.0",
		Result:  result,
	}
	
	return cs.enc.Encode(resp)
}

// sendTCPError sends a JSON-RPC error response over TCP
func (cs *Session) sendTCPError(id json.RawMessage, reply *ErrorReply) error {
	cs.Lock()
	defer cs.Unlock()
	
	resp := JSONRpcResp{
		Id:      id,
		Version: "2.0",
		Error:   reply,
	}
	
	return cs.enc.Encode(resp)
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

// broadcastNewJobs broadcasts new jobs to all connected miners
func (s *ProxyServer) broadcastNewJobs() {
	t := s.currentBlockTemplate()
	if t == nil || len(t.SealHash) == 0 || s.isSick() {
		return
	}

	poolDiff := s.GetPoolShareDifficulty()
	if poolDiff <= 0 {
		poolDiff = DefaultRandomXShareDifficulty
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

	log.Printf("�� Broadcasting new job to %d miners - height=%d, seal=%s...",
		count, t.Height, shortHex(t.SealHash, 16))

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

// setDeadline sets the connection deadline
func (s *ProxyServer) setDeadline(conn *net.TCPConn) {
	conn.SetDeadline(time.Now().Add(ConnectionTimeout))
}

// registerSession adds a session to the active sessions map
func (s *ProxyServer) registerSession(cs *Session) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	s.sessions[cs] = struct{}{}
}

// removeSession removes a session from the active sessions map
func (s *ProxyServer) removeSession(cs *Session) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	delete(s.sessions, cs)
}

// GetPoolShareDifficulty returns the current share difficulty for the pool
func (s *ProxyServer) GetPoolShareDifficulty() int64 {
	if s.config.Proxy.RandomX.Enabled && s.config.Proxy.RandomX.ShareDifficulty > 0 {
		return s.config.Proxy.RandomX.ShareDifficulty
	}
	if s.config.Proxy.Difficulty > 0 {
		return s.config.Proxy.Difficulty
	}
	return DefaultRandomXShareDifficulty
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
