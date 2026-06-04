package proxy

import (
        "encoding/hex"
        "fmt"
	"log"
	"math/big"
	"strconv"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"

	"github.com/sammy007/open-ethereum-pool/rpc"
	"github.com/sammy007/open-ethereum-pool/util"
)

const maxBacklog = 3

type heightDiffPair struct {
	diff   *big.Int
	height uint64
}

type BlockTemplate struct {
	sync.RWMutex
	Header               string
	Seed                 string
	Target               string
	Difficulty           *big.Int
	Height               uint64
	GetPendingBlockCache *rpc.GetBlockReplyPart
	nonces               map[string]bool
	headers              map[string]heightDiffPair
}

type Block struct {
	difficulty  *big.Int
	hashNoNonce common.Hash
	nonce       uint64
	mixDigest   common.Hash
	number      uint64
}

func (b Block) Difficulty() *big.Int     { return b.difficulty }
func (b Block) HashNoNonce() common.Hash { return b.hashNoNonce }
func (b Block) Nonce() uint64            { return b.nonce }
func (b Block) MixDigest() common.Hash   { return b.mixDigest }
func (b Block) NumberU64() uint64        { return b.number }

func (s *ProxyServer) fetchBlockTemplate() {
	rpc := s.rpc()
	t := s.currentBlockTemplate()
	pendingReply, height, diff, err := s.fetchPendingBlock()
	if err != nil {
		log.Printf("Error while refreshing pending block on %s: %s", rpc.Name, err)
		return
	}
	reply, err := rpc.GetWork()
	if err != nil {
		log.Printf("Error while refreshing block template on %s: %s", rpc.Name, err)
		return
	}
	// No need to update, we have fresh job
	if t != nil && t.Header == reply[0] {
		return
	}

	pendingReply.Difficulty = util.ToHex(s.config.Proxy.Difficulty)

	newTemplate := BlockTemplate{
		Header:               reply[0],
		Seed:                 reply[1],
		Target:               reply[2],
		Height:               height,
		Difficulty:           big.NewInt(diff),
		GetPendingBlockCache: pendingReply,
		headers:              make(map[string]heightDiffPair),
	}
	// Copy job backlog and add current one
	newTemplate.headers[reply[0]] = heightDiffPair{
		diff:   util.TargetHexToDiff(reply[2]),
		height: height,
	}
	if t != nil {
		for k, v := range t.headers {
			if v.height > height-maxBacklog {
				newTemplate.headers[k] = v
			}
		}
	}
	s.blockTemplate.Store(&newTemplate)
	log.Printf("New block to mine on %s at height %d / %s", rpc.Name, height, reply[0][0:10])

	// Stratum
	if s.config.Proxy.Stratum.Enabled {
		go s.broadcastNewJobs()
	}
}

func (s *ProxyServer) fetchPendingBlock() (*rpc.GetBlockReplyPart, uint64, int64, error) {
	rpc := s.rpc()
	reply, err := rpc.GetPendingBlock()
	if err != nil {
		log.Printf("Error while refreshing pending block on %s: %s", rpc.Name, err)
		return nil, 0, 0, err
	}
	blockNumber, err := strconv.ParseUint(strings.Replace(reply.Number, "0x", "", -1), 16, 64)
	if err != nil {
		log.Println("Can't parse pending block number")
		return nil, 0, 0, err
	}
	blockDiff, err := strconv.ParseInt(strings.Replace(reply.Difficulty, "0x", "", -1), 16, 64)
	if err != nil {
		log.Println("Can't parse pending block difficulty")
		return nil, 0, 0, err
	}
	return reply, blockNumber, blockDiff, nil
}

func (s *ProxyServer) getRandomXSeedHash(height uint64) ([]byte, error) {
    // Convert height to hex string with 0x prefix
    heightHex := fmt.Sprintf("0x%x", height)
    
    var seedHashHex string
    err := s.rpc().Call(&seedHashHex, "randomx_getSeedHash", heightHex)
    if err != nil {
        return nil, err
    }
    
    // Remove 0x prefix if present
    seedHashHex = strings.TrimPrefix(seedHashHex, "0x")
    return hex.DecodeString(seedHashHex)
}

func (s *ProxyServer) fetchRandomXBlockTemplate() {
    rpc := s.rpc()
    t := s.currentBlockTemplate()

    reply, err := rpc.GetWork()
    if err != nil {
        log.Printf("Error while refreshing RandomX block template on %s: %s", rpc.Name, err)
        return
    }

    // Get current height for epoch calculation
    height := s.getCurrentHeight()
    if height == 0 {
        return
    }

    seedHashBytes, err := s.getRandomXSeedHash(height)
    if err != nil {
        log.Printf("Error getting RandomX seed hash: %v", err)
        return
    }
    
    // Convert seed hash bytes to hex string with 0x prefix
    seedHashStr := "0x" + hex.EncodeToString(seedHashBytes)

    // No need to update, we have fresh job
    if t != nil && t.Header == reply[0] {
        return
    }

    newTemplate := BlockTemplate{
        Header:     reply[0],
        Seed:       seedHashStr,  // Now this is a string
        Target:     reply[2],
        Height:     height,
        Difficulty: util.TargetHexToDiff(reply[2]),
        headers:    make(map[string]heightDiffPair),
    }

    newTemplate.headers[reply[0]] = heightDiffPair{
        diff:   newTemplate.Difficulty,
        height: height,
    }

    if t != nil {
        for k, v := range t.headers {
            if v.height > height-maxBacklog {
                newTemplate.headers[k] = v
            }
        }
    }

    s.blockTemplate.Store(&newTemplate)
    log.Printf("New RandomX block to mine on %s at height %d / %s", rpc.Name, height, reply[0][0:10])

    if s.config.Proxy.Stratum.Enabled {
        go s.broadcastNewJobs()
    }
}

func (s *ProxyServer) getCurrentHeight() uint64 {
    var result string
    err := s.rpc().Call(&result, "eth_blockNumber")
    if err != nil {
        return 0
    }
    height, err := strconv.ParseUint(strings.Replace(result, "0x", "", -1), 16, 64)
    if err != nil {
        return 0
    }
    return height
}
