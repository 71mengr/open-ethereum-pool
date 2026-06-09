package proxy

import (
	"encoding/json"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"github.com/sammy007/open-ethereum-pool/policy"
	"github.com/sammy007/open-ethereum-pool/rpc"
	"github.com/sammy007/open-ethereum-pool/storage"
	"github.com/sammy007/open-ethereum-pool/util"
)

type ProxyServer struct {
	config *Config
	blockTemplate atomic.Value
	upstream      int32
	upstreams     []*rpc.RPCClient
	backend       *storage.RedisClient
	diff          string
	policy        *policy.PolicyServer
	hashrateExpiration time.Duration
	failsCount    int64

	// RandomX
	randomxManager *RandomXManager
	randomxMu      sync.RWMutex

	// Stratum
	sessionsMu sync.RWMutex
	sessions   map[*Session]struct{}
	timeout    time.Duration
}

type Session struct {
	ip    string
	enc   *json.Encoder
	login string

	// Stratum TCP
	sync.Mutex
	conn *net.TCPConn
}

func NewProxy(cfg *Config, backend *storage.RedisClient) *ProxyServer {
	if len(cfg.Name) == 0 {
		log.Fatal("You must set instance name")
	}

	policySrv := policy.Start(&cfg.Proxy.Policy, backend)
	proxy := &ProxyServer{
		config:  cfg,
		backend: backend,
		policy:  policySrv,
	}

	proxy.updateShareTarget()

	if cfg.Proxy.RandomX.Enabled {
		proxy.randomxManager = NewRandomXManager()
		log.Printf("✅ RandomX support enabled (Epoch: %d, Share Diff: %d)",
			cfg.Proxy.RandomX.EpochLength, cfg.Proxy.RandomX.ShareDifficulty)
	}

	// Setup upstreams
	proxy.upstreams = make([]*rpc.RPCClient, len(cfg.Upstream))
	for i, v := range cfg.Upstream {
		proxy.upstreams[i] = rpc.NewRPCClient(v.Name, v.Url, v.Timeout)
		log.Printf("Upstream: %s => %s", v.Name, v.Url)
	}

	if cfg.Proxy.Stratum.Enabled {
		proxy.sessions = make(map[*Session]struct{})
		go proxy.ListenTCP()
	}

	proxy.fetchBlockTemplate()
	proxy.hashrateExpiration = util.MustParseDuration(cfg.Proxy.HashrateExpiration)

	go proxy.startBackgroundJobs()

	return proxy
}

func (s *ProxyServer) startBackgroundJobs() {
	refresh := util.MustParseDuration(s.config.Proxy.BlockRefreshInterval)
	check := util.MustParseDuration(s.config.UpstreamCheckInterval)
	state := util.MustParseDuration(s.config.Proxy.StateUpdateInterval)

	tRefresh := time.NewTimer(refresh)
	tCheck := time.NewTimer(check)
	tState := time.NewTimer(state)

	for {
		select {
		case <-tRefresh.C:
			s.fetchBlockTemplate()
			tRefresh.Reset(refresh)
		case <-tCheck.C:
			s.checkUpstreams()
			tCheck.Reset(check)
		case <-tState.C:
			t := s.currentBlockTemplate()
			if t != nil {
				err := s.backend.WriteNodeState(s.config.Name, t.Height, t.Difficulty)
				if err != nil {
					s.markSick()
				} else {
					s.markOk()
				}
			}
			tState.Reset(state)
		}
	}
}

func (s *ProxyServer) GetPoolShareDifficulty() int64 {
	if s.config.Proxy.RandomX.Enabled && s.config.Proxy.RandomX.ShareDifficulty > 0 {
		return s.config.Proxy.RandomX.ShareDifficulty
	}
	return s.config.Proxy.Difficulty
}

func (s *ProxyServer) updateShareTarget() {
	s.diff = randomXStratumTarget(s.GetPoolShareDifficulty())
}

func (s *ProxyServer) fetchBlockTemplate() {
	if s.isSick() {
		return
	}
	rpcClient := s.rpc()
	reply, err := rpcClient.GetWork()
	if err != nil {
		log.Printf("GetWork failed from %s: %v", rpcClient.Name, err)
		s.markSick()
		return
	}
	if len(reply) < 3 {
		log.Printf("Invalid GetWork reply")
		return
	}

	height := uint64(0)
	if len(reply) > 3 {
		height, _ = util.HexToUint64(reply[3])
	}

	t := &BlockTemplate{
		Header:     reply[0],
		Seed:       reply[1],
		Target:     reply[2],
		Height:     height,
		Difficulty: s.targetToDifficulty(reply[2]),
	}

	s.blockTemplate.Store(t)
	log.Printf("New block template | Height: %d | Seed: %s...", height, shortHex(reply[1], 16))
}

func (s *ProxyServer) targetToDifficulty(targetHex string) *big.Int {
	target := hexToBytes(targetHex)
	if len(target) == 0 {
		return big.NewInt(0)
	}
	tBig := new(big.Int).SetBytes(reverseBytes(target))
	if tBig.Sign() == 0 {
		return big.NewInt(0)
	}
	return new(big.Int).Div(maxUint256, tBig)
}

func (s *ProxyServer) currentBlockTemplate() *BlockTemplate {
	if t := s.blockTemplate.Load(); t != nil {
		return t.(*BlockTemplate)
	}
	return nil
}

func (s *ProxyServer) rpc() *rpc.RPCClient {
	return s.upstreams[atomic.LoadInt32(&s.upstream)]
}

func (s *ProxyServer) checkUpstreams() {
	candidate := int32(0)
	for i, u := range s.upstreams {
		if u.Check() {
			candidate = int32(i)
			break
		}
	}
	if s.upstream != candidate {
		log.Printf("Switching upstream to %s", s.upstreams[candidate].Name)
		atomic.StoreInt32(&s.upstream, candidate)
	}
}

func (s *ProxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		s.writeError(w, 405, "POST method required")
		return
	}
	ip := s.remoteAddr(r)
	if !s.policy.IsBanned(ip) {
		s.handleClient(w, r, ip)
	}
}

func (s *ProxyServer) remoteAddr(r *http.Request) string {
	if s.config.Proxy.BehindReverseProxy {
		if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
			return strings.Split(ip, ",")[0]
		}
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

func (s *ProxyServer) handleClient(w http.ResponseWriter, r *http.Request, ip string) {
	if r.ContentLength > s.config.Proxy.LimitBodySize {
		s.policy.ApplyMalformedPolicy(ip)
		http.Error(w, "Request too large", http.StatusExpectationFailed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.config.Proxy.LimitBodySize)
	defer r.Body.Close()

	cs := &Session{ip: ip, enc: json.NewEncoder(w)}
	dec := json.NewDecoder(r.Body)

	for {
		var req JSONRpcReq
		if err := dec.Decode(&req); err == io.EOF {
			break
		} else if err != nil {
			s.policy.ApplyMalformedPolicy(ip)
			return
		}
		cs.handleMessage(s, r, &req)
	}
}

func (cs *Session) handleMessage(s *ProxyServer, r *http.Request, req *JSONRpcReq) {
	if req.Id == nil {
		return
	}

	vars := mux.Vars(r)
	login := strings.ToLower(vars["login"])
	if !util.IsValidHexAddress(login) {
		cs.sendError(req.Id, &ErrorReply{Code: -1, Message: "Invalid login"})
		return
	}

	if !s.policy.ApplyLoginPolicy(login, cs.ip) {
		cs.sendError(req.Id, &ErrorReply{Code: -1, Message: "Blacklisted"})
		return
	}

	switch req.Method {
	case "eth_getWork":
		reply, errReply := s.handleGetWorkRPC(s)
		if errReply != nil {
			cs.sendError(req.Id, errReply)
		} else {
			cs.sendResult(req.Id, reply)
		}
	case "eth_submitWork":
		var params []string
		json.Unmarshal(req.Params, &params)
		reply, errReply := s.handleSubmitRPC(cs, login, vars["id"], params)
		if errReply != nil {
			cs.sendError(req.Id, errReply)
		} else {
			cs.sendResult(req.Id, reply)
		}
	case "eth_submitHashrate":
		cs.sendResult(req.Id, true)
	default:
		cs.sendError(req.Id, s.handleUnknownRPC(cs, req.Method))
	}
}

func (s *ProxyServer) handleSubmitRPC(cs *Session, login, workerID string, params []string) (interface{}, *ErrorReply) {
	t := s.currentBlockTemplate()
	if t == nil {
		return false, &ErrorReply{Code: -1, Message: "No work available"}
	}

	validShare, blockFound := s.processShare(login, workerID, cs.ip, t, params)
	if validShare {
		return true, nil
	}
	return false, &ErrorReply{Code: 21, Message: "Low difficulty share"}
}

func (s *ProxyServer) handleGetWorkRPC(cs *Session) (interface{}, *ErrorReply) {
	t := s.currentBlockTemplate()
	if t == nil {
		return nil, &ErrorReply{Code: -1, Message: "No work available"}
	}
	header := strings.TrimPrefix(t.Header, "0x")
	seed := strings.TrimPrefix(t.Seed, "0x")
	target := s.diff
	return []string{"0x" + header, "0x" + seed, "0x" + target}, nil
}

func (s *ProxyServer) handleUnknownRPC(cs *Session, method string) *ErrorReply {
	log.Printf("Unknown method %s from %s", method, cs.ip)
	return &ErrorReply{Code: -3, Message: "Method not found"}
}

func (s *ProxyServer) markSick() {
	atomic.AddInt64(&s.failsCount, 1)
}

func (s *ProxyServer) markOk() {
	atomic.StoreInt64(&s.failsCount, 0)
}

func (s *ProxyServer) isSick() bool {
	if !s.config.Proxy.HealthCheck {
		return false
	}
	return atomic.LoadInt64(&s.failsCount) >= s.config.Proxy.MaxFails
}

func (cs *Session) sendResult(id json.RawMessage, result interface{}) error {
	return cs.enc.Encode(JSONRpcResp{Id: id, Version: "2.0", Result: result})
}

func (cs *Session) sendError(id json.RawMessage, reply *ErrorReply) error {
	return cs.enc.Encode(JSONRpcResp{Id: id, Version: "2.0", Error: reply})
}

func (s *ProxyServer) writeError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(msg))
}
