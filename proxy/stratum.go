package proxy

import (
        "bufio"
        "encoding/json"
        "errors"
        "io"
        "log"
        "net"
        "time"
        "strings"
        "math/big"
        "github.com/sammy007/open-ethereum-pool/util"
)

const (
        MaxReqSize = 1024
)

func (s *ProxyServer) ListenTCP() {
        timeout := util.MustParseDuration(s.config.Proxy.Stratum.Timeout)
        s.timeout = timeout

        addr, err := net.ResolveTCPAddr("tcp", s.config.Proxy.Stratum.Listen)
        if err != nil {
                log.Fatalf("Error: %v", err)
        }
        server, err := net.ListenTCP("tcp", addr)
        if err != nil {
                log.Fatalf("Error: %v", err)
        }
        defer server.Close()

        log.Printf("Stratum listening on %s", s.config.Proxy.Stratum.Listen)
        var accept = make(chan int, s.config.Proxy.Stratum.MaxConn)
        n := 0

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
                n += 1
                cs := &Session{conn: conn, ip: ip}

                accept <- n
                go func(cs *Session) {
                        err = s.handleTCPClient(cs)
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
        connbuff := bufio.NewReaderSize(cs.conn, MaxReqSize)
        s.setDeadline(cs.conn)

        for {
                data, isPrefix, err := connbuff.ReadLine()
                if isPrefix {
                        log.Printf("Socket flood detected from %s", cs.ip)
                        s.policy.BanClient(cs.ip)
                        return err
                } else if err == io.EOF {
                        log.Printf("Client %s disconnected", cs.ip)
                        s.removeSession(cs)
                        break
                } else if err != nil {
                        log.Printf("Error reading from socket: %v", err)
                        return err
                }

                if len(data) > 1 {
                        var req StratumReq
                        err = json.Unmarshal(data, &req)
                        if err != nil {
                                s.policy.ApplyMalformedPolicy(cs.ip)
                                log.Printf("Malformed stratum request from %s: %v", cs.ip, err)
                                return err
                        }
                        s.setDeadline(cs.conn)
                        err = cs.handleTCPMessage(s, &req)
                        if err != nil {
                                return err
                        }
                }
        }
        return nil
}

func (cs *Session) handleTCPMessage(s *ProxyServer, req *StratumReq) error {
        // Handle RPC methods
        switch req.Method {
        // XMRig/CryptoNote methods for RandomX
case "login":
        var objParams map[string]interface{}
        if err := json.Unmarshal(req.Params, &objParams); err != nil {
                log.Printf("Failed to parse login params from %s: %v", cs.ip, err)
                return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Invalid login params"})
        }
        
        login, ok := objParams["login"].(string)
        if !ok {
                login, ok = objParams["user"].(string)
        }
        if !ok {
                return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Invalid login params"})
        }
        
        log.Printf("XMRig login from %s: user=%s", cs.ip, login)
        
        t := s.currentBlockTemplate()
        if t == nil || len(t.Header) == 0 {
                return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Work not ready"})
        }
        
        // Remove 0x prefix from all hex fields
        header := strings.TrimPrefix(t.Header, "0x")
        seedHash := strings.TrimPrefix(t.Seed, "0x")
        target := strings.TrimPrefix(t.Target, "0x")
        
        // Ensure target is 64 chars (pad with leading zeros if needed)
        for len(target) < 64 {
                target = "0" + target
        }
        
        job := map[string]interface{}{
                "blob":      header,
                "job_id":    "1",
                "target":    target,
                "seed_hash": seedHash,
                "height":    t.Height,
        }
        
        response := map[string]interface{}{
                "id":     login,
                "job":    job,
                "status": "OK",
        }
        
        log.Printf("Login response for %s: blob=%s..., target=%s", cs.ip, header[:32], target[:16])
        
        cs.login = login
        s.registerSession(cs)
        
        return cs.sendTCPResult(req.Id, response)

case "job":
    t := s.currentBlockTemplate()
    if t == nil || len(t.Header) == 0 {
        return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Work not ready"})
    }
    
    // Remove 0x prefix for XMRig
    header := strings.TrimPrefix(t.Header, "0x")
    seedHash := strings.TrimPrefix(t.Seed, "0x")
    target := strings.TrimPrefix(t.Target, "0x")
    
    // Ensure target is exactly 64 hex characters
    for len(target) < 64 {
        target = "0" + target
    }
    if len(target) > 64 {
        target = target[:64]
    }
    
    job := map[string]interface{}{
        "blob":      header,
        "job_id":    "1",
        "target":    target,      // XMRig expects 64-char hex WITHOUT 0x
        "seed_hash": seedHash,
        "height":    t.Height,
    }
    
    log.Printf("Sending job to %s: target=%s (len=%d)", cs.ip, target[:16], len(target))
    return cs.sendTCPResult(req.Id, job)

        // Original Ethereum methods
        case "eth_submitLogin":
                var params []string
                err := json.Unmarshal(req.Params, &params)
                if err != nil {
                        log.Println("Malformed stratum request params from", cs.ip)
                        return err
                }
                reply, errReply := s.handleLoginRPC(cs, params, req.Worker)
                if errReply != nil {
                        return cs.sendTCPError(req.Id, errReply)
                }
                return cs.sendTCPResult(req.Id, reply)

        case "eth_getWork":
                reply, errReply := s.handleGetWorkRPC(cs)
                if errReply != nil {
                        return cs.sendTCPError(req.Id, errReply)
                }
                return cs.sendTCPResult(req.Id, &reply)

case "eth_submitWork":
        var params []string
        if err := json.Unmarshal(req.Params, &params); err != nil {
                log.Println("Malformed stratum submitWork params from", cs.ip)
                return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Invalid params"})
        }
        
        // eth_submitWork params: [nonce, headerHash, mixDigest]
        if len(params) < 3 {
                log.Printf("Invalid eth_submitWork params from %s: expected 3, got %d", cs.ip, len(params))
                return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Invalid params"})
        }
        
        nonce := params[0]
        headerHash := params[1]
        mixDigest := params[2]
        
        log.Printf("eth_submitWork from %s: nonce=%s, header=%s, mix=%s", cs.ip, nonce, headerHash[:16], mixDigest[:16])
        
        // Get current block template
        t := s.currentBlockTemplate()
        if t == nil {
                log.Printf("No block template for eth_submitWork from %s", cs.ip)
                return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Block template expired"})
        }
        
        // Verify header hash matches current job
        if headerHash != t.Header && "0x"+headerHash != t.Header {
                log.Printf("Header mismatch from %s: expected %s, got %s", cs.ip, t.Header, headerHash)
                return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Invalid header hash"})
        }
        
        // Process the share
        params = []string{nonce, headerHash, mixDigest}
        valid, accepted := s.processShare(cs.login, "", cs.ip, t, params)
        
        if !valid {
                log.Printf("Invalid eth_submitWork share from %s", cs.ip)
                return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Invalid share"})
        }
        
        if accepted {
                log.Printf("Block found by %s!", cs.ip)
        } else {
                log.Printf("Share accepted from %s", cs.ip)
        }
        
        return cs.sendTCPResult(req.Id, true)

        case "eth_submitHashrate":
                return cs.sendTCPResult(req.Id, true)

        default:
                log.Printf("Unknown method from %s: %s", cs.ip, req.Method)
                errReply := s.handleUnknownRPC(cs, req.Method)
                return cs.sendTCPError(req.Id, errReply)
        }
}

func (cs *Session) sendTCPResult(id json.RawMessage, result interface{}) error {
        cs.Lock()
        defer cs.Unlock()

        message := JSONRpcResp{Id: id, Version: "2.0", Error: nil, Result: result}
        return cs.enc.Encode(&message)
}

func (cs *Session) pushNewJob(result interface{}) error {
        cs.Lock()
        defer cs.Unlock()
        // FIXME: Temporarily add ID for Claymore compliance
        message := JSONPushMessage{Version: "2.0", Result: result, Id: 0}
        return cs.enc.Encode(&message)
}

func (cs *Session) sendTCPError(id json.RawMessage, reply *ErrorReply) error {
        cs.Lock()
        defer cs.Unlock()

        message := JSONRpcResp{Id: id, Version: "2.0", Error: reply}
        err := cs.enc.Encode(&message)
        if err != nil {
                return err
        }
        return errors.New(reply.Message)
}

func (self *ProxyServer) setDeadline(conn *net.TCPConn) {
        conn.SetDeadline(time.Now().Add(self.timeout))
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

func (s *ProxyServer) broadcastNewJobs() {
        t := s.currentBlockTemplate()
        if t == nil || len(t.Header) == 0 || s.isSick() {
                return
        }
        reply := []string{t.Header, t.Seed, s.diff}

        s.sessionsMu.RLock()
        defer s.sessionsMu.RUnlock()

        count := len(s.sessions)
        log.Printf("Broadcasting new job to %v stratum miners", count)

        start := time.Now()
        bcast := make(chan int, 1024)
        n := 0

        for m, _ := range s.sessions {
                n++
                bcast <- n

                go func(cs *Session) {
                        err := cs.pushNewJob(&reply)
                        <-bcast
                        if err != nil {
                                log.Printf("Job transmit error to %v@%v: %v", cs.login, cs.ip, err)
                                s.removeSession(cs)
                        } else {
                                s.setDeadline(cs.conn)
                        }
                }(m)
        }
        log.Printf("Jobs broadcast finished %s", time.Since(start))
}

func (cs *Session) handleGetWorkRPC(s *ProxyServer) ([]string, *ErrorReply) {
    t := s.currentBlockTemplate()
    if t == nil || len(t.Header) == 0 {
        return nil, &ErrorReply{Code: -1, Message: "Work not ready"}
    }

    header := strings.TrimPrefix(t.Header, "0x")
    seed := strings.TrimPrefix(t.Seed, "0x")
    target := strings.TrimPrefix(t.Target, "0x")

    // If seed is all zeros, use header as seed (temporary fix for RandomX)
    allZero := true
    for _, c := range seed {
        if c != '0' {
            allZero = false
            break
        }
    }
    if allZero || len(seed) == 0 {
        seed = header
        log.Printf("Seed was zero, using header as seed for %s", cs.ip)
    }

    // Calculate difficulty from target
    // maxUint256 = 2^256 - 1
    maxUint256 := new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil)
    maxUint256.Sub(maxUint256, big.NewInt(1))

    targetBig := new(big.Int)
    targetBig.SetString(target, 16)
    
    // Avoid division by zero
    if targetBig.Sign() == 0 {
        targetBig.SetUint64(1)
    }
    
    difficulty := new(big.Int).Div(maxUint256, targetBig)

    // Ensure difficulty is at least 1
    if difficulty.Sign() == 0 {
        difficulty.SetUint64(1)
    }

    diffStr := difficulty.String()

    log.Printf("eth_getWork for %s: target=%s..., difficulty=%s", cs.ip, target[:16], diffStr[:min(10, len(diffStr))])

    // Return with 0x prefix for header and seed, plain decimal for difficulty
    reply := []string{"0x" + header, "0x" + seed, diffStr}
    return reply, nil
}

// Helper function
func min(a, b int) int {
    if a < b {
        return a
    }
    return b
}

func formatTarget(target string) string {
    target = strings.TrimPrefix(target, "0x")
    // Pad to 64 characters
    for len(target) < 64 {
        target = "0" + target
    }
    if len(target) > 64 {
        target = target[:64]
    }
    return target
}
