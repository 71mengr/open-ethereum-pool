//go:build randomx

package proxy

import "log"

func (s *ProxyServer) processFallbackShare(login, id, ip string, t *BlockTemplate, params []string) (bool, bool) {
        log.Printf("Rejected non-RandomX share from %v@%v: RandomX build does not include Ethash verification", login, ip)
        return false, false
}
