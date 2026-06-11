package proxy

import (
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"strings"
)

var (
	maxUint256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
)

// randomXStratumTarget converts difficulty to 64-character hex target for RandomX
func randomXStratumTarget(diff int64) string {
	if diff <= 0 {
		diff = 1000
	}
	target := new(big.Int).Div(maxUint256, big.NewInt(diff))
	return fmt.Sprintf("%064x", target)
}

// formatRandomXTarget exports the target formatter
func formatRandomXTarget(diff int64) string {
	return randomXStratumTarget(diff)
}

// processTKMShare processes a TKM share submission and submits blocks to daemon
func (s *ProxyServer) processTKMShare(login, id, ip string, t *BlockTemplate, nonceHex, sealHashHex, mixDigestHex string) (bool, bool) {
	if t == nil {
		log.Printf("  [ERROR] No block template for share from %s", ip)
		return false, false
	}

	log.Printf("⛏️ Share from %s - nonce=%s, height=%d", ip, nonceHex, t.Height)

	// Prepare params for submission
	params := []string{nonceHex, add0x(t.SealHash), add0x(mixDigestHex)}

	// Submit to daemon (daemon will verify PoW)
	ok, err := s.rpc().SubmitBlock(params)
	if err != nil {
		log.Printf("  [ERROR] Block submission error: %v", err)
		// Still store as share in backend
		_, err = s.backend.WriteShare(login, id, params, 1, t.Height, s.hashrateExpiration)
		if err != nil {
			return false, false
		}
		return false, true
	}

	if ok {
		log.Printf("  ✅✅✅ BLOCK ACCEPTED BY DAEMON! Height: %d", t.Height)
		// Store as block in backend
		_, err = s.backend.WriteBlock(login, id, params, 1, t.Difficulty.Int64(), t.Height, s.hashrateExpiration)
		if err != nil {
			return false, false
		}
		// Fetch new template after block found
		go s.fetchBlockTemplate()
		return false, true
	} else {
		log.Printf("  ⚠️ Share accepted but not a block (difficulty too low)")
		// Store as regular share
		_, err = s.backend.WriteShare(login, id, params, 1, t.Height, s.hashrateExpiration)
		if err != nil {
			return false, false
		}
		return false, true
	}
}

// processShare handles standard share processing (for backward compatibility)
func (s *ProxyServer) processShare(login, id, ip string, t *BlockTemplate, params []string) (bool, bool) {
	if len(params) < 3 {
		return false, false
	}
	return s.processTKMShare(login, id, ip, t, params[0], params[1], params[2])
}

// hexToBytes converts a hex string to bytes
func hexToBytes(s string) []byte {
	s = strings.TrimPrefix(s, "0x")
	b, _ := hex.DecodeString(s)
	return b
}

// add0x adds 0x prefix if not present
func add0x(s string) string {
	if !strings.HasPrefix(s, "0x") && len(s) > 0 {
		return "0x" + s
	}
	return s
}

// shortHex returns a short hex string for logging
func shortHex(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
