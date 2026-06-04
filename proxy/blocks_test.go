package proxy

import "testing"

func TestGetPoolShareDifficultyUsesRandomXDefault(t *testing.T) {
	server := &ProxyServer{}
	server.config.Proxy.Difficulty = 2000000000
	server.config.Proxy.RandomX.Enabled = true

	got := server.GetPoolShareDifficulty()
	if got != DefaultRandomXShareDifficulty {
		t.Fatalf("expected RandomX default share difficulty %d, got %d", DefaultRandomXShareDifficulty, got)
	}
}

func TestGetPoolShareDifficultyUsesConfiguredRandomXShareDifficulty(t *testing.T) {
	server := &ProxyServer{}
	server.config.Proxy.Difficulty = 2000000000
	server.config.Proxy.RandomX.Enabled = true
	server.config.Proxy.RandomX.ShareDifficulty = 5000

	got := server.GetPoolShareDifficulty()
	if got != 5000 {
		t.Fatalf("expected configured RandomX share difficulty 5000, got %d", got)
	}
}

func TestGetPoolShareDifficultyUsesProxyDifficultyForEthash(t *testing.T) {
	server := &ProxyServer{}
	server.config.Proxy.Difficulty = 2000000000

	got := server.GetPoolShareDifficulty()
	if got != server.config.Proxy.Difficulty {
		t.Fatalf("expected proxy difficulty %d, got %d", server.config.Proxy.Difficulty, got)
	}
}
