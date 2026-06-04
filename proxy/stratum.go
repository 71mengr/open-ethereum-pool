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
        target := randomXStratumTarget(s.GetPoolShareDifficulty())

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
    target := randomXStratumTarget(s.GetPoolShareDifficulty())

    // Force epoch 0 seed hash for heights < 2048
    var finalSeedHash string
    if t.Height < 2048 {
        finalSeedHash = "0000000000000000000000000000000000000000000000000000000000000000"
        log.Printf("Using forced epoch 0 seed hash for height %d", t.Height)
    } else {
        finalSeedHash = seedHash
    }

    // XMRig expects these exact field names
    job := map[string]interface{}{
        "blob":      header,           // The block header hash (without nonce)
        "job_id":    "1",              // Job ID
        "target":    target,           // Pool target (compact little-endian hex WITHOUT 0x)
        "seed_hash": finalSeedHash,    // CRITICAL: Must match what XMRig expects
        "height":    t.Height,         // Current block height
    }

    log.Printf("Sending job to %s: height=%d, seed_hash=%s, target=%s",
        cs.ip, t.Height, finalSeedHash[:16], target[:16])

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

    if len(params) < 3 {
        log.Printf("Invalid eth_submitWork params from %s: expected 3, got %d", cs.ip, len(params))
        return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Invalid params"})
    }

    nonce := params[0]
    secondParam := params[1]  // This is the header hash the miner used
    mixDigest := params[2]

    log.Printf("eth_submitWork from %s: nonce=%s, headerHash=%s, mix=%s", cs.ip, nonce, shortHex(secondParam, 16), shortHex(mixDigest, 16))

    // Get current block template
    t := s.currentBlockTemplate()
    if t == nil {
        log.Printf("No block template for eth_submitWork from %s", cs.ip)
        return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Block template expired"})
    }

    // Preserve the miner's submitted header hash for share processing.
    params = []string{nonce, secondParam, mixDigest}

    exist, validShare := s.processShare(cs.login, "", cs.ip, t, params)

    if exist {
        log.Printf("Duplicate eth_submitWork share from %s", cs.ip)
        return cs.sendTCPError(req.Id, &ErrorReply{Code: 22, Message: "Duplicate share"})
    }

    if !validShare {
        log.Printf("Invalid eth_submitWork share from %s", cs.ip)
        return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Invalid share"})
    }

    log.Printf("Share accepted from %s", cs.ip)

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
    
    // Remove 0x prefix for miners
    header := strings.TrimPrefix(t.Header, "0x")  // This is the actual header hash
    seed := strings.TrimPrefix(t.Seed, "0x")      // This is the seed hash for RandomX
    target := randomXStratumTarget(s.GetPoolShareDifficulty())
    
    reply := []string{header, seed, target}

    s.sessionsMu.RLock()
    defer s.sessionsMu.RUnlock()

    //count := len(s.sessions)
    log.Printf("Broadcasting job - Height: %d, Header: %s..., Seed: %s...", 
        t.Height, header[:16], seed[:16])

    start := time.Now()
    bcast := make(chan int, 1024)
    n := 0

    for m := range s.sessions {
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
    target := formatTarget(s.diff)

    log.Printf("eth_getWork for %s: target=%s...", cs.ip, target[:16])

    // Return the configured pool share target as the getWork boundary.
    reply := []string{"0x" + header, "0x" + seed, "0x" + target}
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
