package proxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/sammy007/open-ethereum-pool/util"
)

const MaxReqSize = 1024

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
	case "login":
		// Parse login params
		var params map[string]interface{}
		json.Unmarshal(req.Params, &params)
		login, _ := params["login"].(string)
		if login == "" {
			login, _ = params["user"].(string)
		}

		t := s.currentBlockTemplate()
		if t == nil {
			return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Work not ready"})
		}

		job := map[string]interface{}{
			"blob":      strings.TrimPrefix(t.Header, "0x"),
			"job_id":    "1",
			"target":    randomXStratumTarget(s.GetPoolShareDifficulty()),
			"seed_hash": strings.TrimPrefix(t.Seed, "0x"),
			"height":    t.Height,
		}

		response := map[string]interface{}{
			"id":     login,
			"job":    job,
			"status": "OK",
		}

		cs.login = login
		s.registerSession(cs)
		return cs.sendTCPResult(req.Id, response)

	case "job":
		t := s.currentBlockTemplate()
		if t == nil {
			return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Work not ready"})
		}

		job := map[string]interface{}{
			"blob":      strings.TrimPrefix(t.Header, "0x"),
			"job_id":    "1",
			"target":    randomXStratumTarget(s.GetPoolShareDifficulty()),
			"seed_hash": strings.TrimPrefix(t.Seed, "0x"),
			"height":    t.Height,
		}
		return cs.sendTCPResult(req.Id, job)

	case "eth_submitWork":
		var params []string
		json.Unmarshal(req.Params, &params)
		if len(params) < 3 {
			return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "Invalid params"})
		}

		t := s.currentBlockTemplate()
		if t == nil {
			return cs.sendTCPError(req.Id, &ErrorReply{Code: -1, Message: "No template"})
		}

		exist, valid := s.processShare(cs.login, "", cs.ip, t, params)
		if exist {
			return cs.sendTCPError(req.Id, &ErrorReply{Code: 22, Message: "Duplicate share"})
		}
		if !valid {
			return cs.sendTCPResult(req.Id, false)
		}
		return cs.sendTCPResult(req.Id, true)

	case "eth_submitHashrate":
		return cs.sendTCPResult(req.Id, true)

	default:
		return cs.sendTCPError(req.Id, s.handleUnknownRPC(cs, req.Method))
	}
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

func (cs *Session) pushNewJob(result interface{}) error {
	cs.Lock()
	defer cs.Unlock()
	return cs.enc.Encode(JSONPushMessage{Version: "2.0", Result: result, Id: 0})
}

func (s *ProxyServer) broadcastNewJobs() {
	t := s.currentBlockTemplate()
	if t == nil || len(t.Header) == 0 || s.isSick() {
		return
	}

	job := map[string]interface{}{
		"blob":      strings.TrimPrefix(t.Header, "0x"),
		"job_id":    "1",
		"target":    randomXStratumTarget(s.GetPoolShareDifficulty()),
		"seed_hash": strings.TrimPrefix(t.Seed, "0x"),
		"height":    t.Height,
	}

	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()

	for session := range s.sessions {
		go func(cs *Session) {
			if err := cs.pushNewJob(job); err != nil {
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

// Types
type StratumReq struct {
	Id     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Worker string          `json:"worker"`
}

type JSONPushMessage struct {
	Version string      `json:"jsonrpc"`
	Result  interface{} `json:"result"`
	Id      int         `json:"id"`
}
