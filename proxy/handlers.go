package proxy

import (
        "encoding/json"
        "log"
        "regexp"
        "strings"
//        "time"

        "github.com/sammy007/open-ethereum-pool/rpc"
        "github.com/sammy007/open-ethereum-pool/util"
)

// Allow only lowercase hexadecimal with 0x prefix
var noncePattern = regexp.MustCompile("^0x[0-9a-f]{16}$")
var hashPattern = regexp.MustCompile("^0x[0-9a-f]{64}$")
var workerPattern = regexp.MustCompile("^[0-9a-zA-Z-_]{1,8}$")

// formatTarget converts difficulty to the compact little-endian target used
// by XMRig-compatible RandomX stratum jobs.
func formatTarget(diff int64) string {
        return randomXStratumTarget(diff)
}

// add0x adds 0x prefix if not present
/*func add0x(s string) string {
        if !strings.HasPrefix(s, "0x") && len(s) > 0 {
                return "0x" + s
        }
        return s
}

// remove0x removes 0x prefix if present
func remove0x(s string) string {
        return strings.TrimPrefix(s, "0x")
}
*/
// Stratum login handler for XMRig
func (s *ProxyServer) handleLoginRPC(cs *Session, params []string, id string) (bool, *ErrorReply) {
        if len(params) == 0 {
                return false, &ErrorReply{Code: -1, Message: "Invalid params"}
        }

        login := strings.ToLower(params[0])
        if !util.IsValidHexAddress(login) {
                return false, &ErrorReply{Code: -1, Message: "Invalid login"}
        }
        if !s.policy.ApplyLoginPolicy(login, cs.ip) {
                return false, &ErrorReply{Code: -1, Message: "You are blacklisted"}
        }
        cs.login = login
        s.registerSession(cs)
        log.Printf("Stratum miner connected %v@%v", login, cs.ip)
        return true, nil
}

// XMRig login handler (JSON object format)
func (s *ProxyServer) handleXMRigLogin(cs *Session, params map[string]interface{}, id json.RawMessage) error {
        login, ok := params["login"].(string)
        if !ok {
                login, _ = params["user"].(string)
        }
        if login == "" {
                return cs.sendTCPError(id, &ErrorReply{Code: -1, Message: "Login required"})
        }

        pass, _ := params["pass"].(string)
        if pass == "" {
                pass = "x"
        }

        if !util.IsValidHexAddress(login) {
                return cs.sendTCPError(id, &ErrorReply{Code: -1, Message: "Invalid address"})
        }

        if !s.policy.ApplyLoginPolicy(login, cs.ip) {
                return cs.sendTCPError(id, &ErrorReply{Code: -1, Message: "You are blacklisted"})
        }

        cs.login = login
        s.registerSession(cs)

        // Get current work
        t := s.currentBlockTemplate()
        if t == nil || len(t.Header) == 0 {
                return cs.sendTCPError(id, &ErrorReply{Code: -1, Message: "Work not ready"})
        }

        // Format job for XMRig
        target := formatTarget(s.GetPoolShareDifficulty())
        job := map[string]interface{}{
                "blob":      remove0x(t.Header),
                "job_id":    "1",
                "target":    target,
                "seed_hash": remove0x(t.Seed),
                "height":    t.Height,
        }

        result := map[string]interface{}{
                "id":     login,
                "job":    job,
                "status": "OK",
        }

        log.Printf("✅ XMRig login: %s@%s target=%s", login, cs.ip, target)
        return cs.sendTCPResult(id, result)
}

// GetWork handler for Ethereum-compatible miners
func (s *ProxyServer) handleGetWorkRPC(cs *Session) ([]string, *ErrorReply) {
        t := s.currentBlockTemplate()
        if t == nil || len(t.Header) == 0 || s.isSick() {
                return nil, &ErrorReply{Code: 0, Message: "Work not ready"}
        }

        log.Printf("�� Sending eth_getWork to %s: header=%s..., seed=%s..., target=%s...",
                cs.ip, t.Header[:16], t.Seed[:16], s.diff[:16])

        return []string{t.Header, t.Seed, s.diff}, nil
}

// Job request handler for XMRig
func (s *ProxyServer) handleJobRequest(cs *Session, id json.RawMessage) error {
        t := s.currentBlockTemplate()
        if t == nil || len(t.Header) == 0 {
                return cs.sendTCPError(id, &ErrorReply{Code: -1, Message: "Work not ready"})
        }

        target := formatTarget(s.GetPoolShareDifficulty())
        job := map[string]interface{}{
                "blob":      remove0x(t.Header),
                "job_id":    "1",
                "target":    target,
                "seed_hash": remove0x(t.Seed),
                "height":    t.Height,
        }

        log.Printf("�� Sending job to %s: blob=%s..., target=%s...",
                cs.ip, remove0x(t.Header)[:16], target)
        return cs.sendTCPResult(id, job)
}

// Submit handler for XMRig (wallet, jobId, nonce, result)
func (s *ProxyServer) handleXMRigSubmit(cs *Session, params []string, id json.RawMessage) error {
        if len(params) < 4 {
                return cs.sendTCPError(id, &ErrorReply{Code: -1, Message: "Invalid params"})
        }

        wallet := params[0]
        jobId := params[1]
        nonceHex := params[2]
        resultHashHex := params[3]

        log.Printf("�� XMRig submit from %s: nonce=%s, result=%s...",
                wallet, nonceHex, resultHashHex[:16])

        // Get current block template
        t := s.currentBlockTemplate()
        if t == nil {
                return cs.sendTCPError(id, &ErrorReply{Code: -1, Message: "Block template expired"})
        }

        // Convert to eth_submitWork format: [nonce, headerHash, mixDigest]
        ethParams := []string{
                add0x(nonceHex),
                t.Header,
                add0x(resultHashHex),
        }

        exist, validShare := s.processShare(wallet, jobId, cs.ip, t, ethParams)

        if exist {
                return cs.sendTCPError(id, &ErrorReply{Code: 22, Message: "Duplicate share"})
        }

        if !validShare {
                return cs.sendTCPError(id, &ErrorReply{Code: -1, Message: "Invalid share"})
        }

        log.Printf("✅ Share accepted from %s@%s", wallet, cs.ip)
        return cs.sendTCPResult(id, true)
}

// TCP submit handler for Ethereum-compatible miners
func (s *ProxyServer) handleTCPSubmitRPC(cs *Session, id string, params []string) (bool, *ErrorReply) {
        s.sessionsMu.RLock()
        _, ok := s.sessions[cs]
        s.sessionsMu.RUnlock()

        if !ok {
                return false, &ErrorReply{Code: 25, Message: "Not subscribed"}
        }
        return s.handleSubmitRPC(cs, cs.login, id, params)
}

// Handle eth_submitWork from miners
func (s *ProxyServer) handleSubmitRPC(cs *Session, login, id string, params []string) (bool, *ErrorReply) {
        if !workerPattern.MatchString(id) {
                id = "0"
        }
        if len(params) != 3 {
                s.policy.ApplyMalformedPolicy(cs.ip)
                log.Printf("Malformed params from %s@%s %v", login, cs.ip, params)
                return false, &ErrorReply{Code: -1, Message: "Invalid params"}
        }

        // Validate nonce (8 bytes)
        if !noncePattern.MatchString(params[0]) {
                s.policy.ApplyMalformedPolicy(cs.ip)
                log.Printf("Malformed nonce from %s@%s %v", login, cs.ip, params[0])
                return false, &ErrorReply{Code: -1, Message: "Malformed nonce"}
        }

        // Validate header hash (32 bytes)
        if !hashPattern.MatchString(params[1]) {
                s.policy.ApplyMalformedPolicy(cs.ip)
                log.Printf("Malformed header hash from %s@%s %v", login, cs.ip, params[1])
                return false, &ErrorReply{Code: -1, Message: "Malformed header hash"}
        }

        // Validate mix digest (32 bytes)
        if !hashPattern.MatchString(params[2]) {
                s.policy.ApplyMalformedPolicy(cs.ip)
                log.Printf("Malformed mix digest from %s@%s %v", login, cs.ip, params[2])
                return false, &ErrorReply{Code: -1, Message: "Malformed mix digest"}
        }

        t := s.currentBlockTemplate()
        exist, validShare := s.processShare(login, id, cs.ip, t, params)
        ok := s.policy.ApplySharePolicy(cs.ip, !exist && validShare)

        if exist {
                log.Printf("Duplicate share from %s@%s %v", login, cs.ip, params)
                return false, &ErrorReply{Code: 22, Message: "Duplicate share"}
        }

        if !validShare {
                log.Printf("Invalid share from %s@%s", login, cs.ip)
                if !ok {
                        return false, &ErrorReply{Code: 23, Message: "Invalid share"}
                }
                return false, nil
        }

        log.Printf("✅ Valid share from %s@%s", login, cs.ip)

        if !ok {
                return true, &ErrorReply{Code: -1, Message: "High rate of invalid shares"}
        }
        return true, nil
}

// Keepalive handler
func (s *ProxyServer) handleKeepalive(cs *Session, id json.RawMessage) error {
        return cs.sendTCPResult(id, "OK")
}

// Get block by number handler
func (s *ProxyServer) handleGetBlockByNumberRPC() *rpc.GetBlockReplyPart {
        t := s.currentBlockTemplate()
        var reply *rpc.GetBlockReplyPart
        if t != nil {
                reply = t.GetPendingBlockCache
        }
        return reply
}

// Unknown RPC method handler
func (s *ProxyServer) handleUnknownRPC(cs *Session, m string) *ErrorReply {
        log.Printf("Unknown request method %s from %s", m, cs.ip)
        s.policy.ApplyMalformedPolicy(cs.ip)
        return &ErrorReply{Code: -3, Message: "Method not found"}
}

// Broadcast new job to all connected miners
func (s *ProxyServer) broadcastNewJob() {
        t := s.currentBlockTemplate()
        if t == nil || len(t.Header) == 0 || s.isSick() {
                return
        }

        target := formatTarget(s.GetPoolShareDifficulty())
        
        job := map[string]interface{}{
                "blob":      remove0x(t.Header),
                "job_id":    "1",
                "target":    target,
                "seed_hash": remove0x(t.Seed),
                "height":    t.Height,
        }

        s.sessionsMu.RLock()
        defer s.sessionsMu.RUnlock()

        count := 0
        for cs := range s.sessions {
                count++
                go func(session *Session) {
                        if err := session.pushNewJob(job); err != nil {
                                log.Printf("Failed to push job to %s: %v", session.ip, err)
                        } else {
                                s.setDeadline(session.conn)
                        }
                }(cs)
        }

        if count > 0 {
                log.Printf("�� Broadcast new job to %d miners - height=%d, target=%s...",
                        count, t.Height, target)
        }
}
