package proxy

import "testing"

func TestGetPoolShareDifficultyUsesRandomXDefault(t *testing.T) {
	server := &ProxyServer{config: &Config{}}
	server.config.Proxy.Difficulty = 2000000000
	server.config.Proxy.RandomX.Enabled = true

	got := server.GetPoolShareDifficulty()
	if got != DefaultRandomXShareDifficulty {
		t.Fatalf("expected RandomX default share difficulty %d, got %d", DefaultRandomXShareDifficulty, got)
	}
}

func TestGetPoolShareDifficultyUsesConfiguredRandomXShareDifficulty(t *testing.T) {
	server := &ProxyServer{config: &Config{}}
	server.config.Proxy.Difficulty = 2000000000
	server.config.Proxy.RandomX.Enabled = true
	server.config.Proxy.RandomX.ShareDifficulty = 5000

	got := server.GetPoolShareDifficulty()
	if got != 5000 {
		t.Fatalf("expected configured RandomX share difficulty 5000, got %d", got)
	}
}

func TestGetPoolShareDifficultyUsesProxyDifficultyForEthash(t *testing.T) {
	server := &ProxyServer{config: &Config{}}
	server.config.Proxy.Difficulty = 2000000000

	got := server.GetPoolShareDifficulty()
	if got != server.config.Proxy.Difficulty {
		t.Fatalf("expected proxy difficulty %d, got %d", server.config.Proxy.Difficulty, got)
	}
}

func TestGetPoolShareTargetUsesRandomXShareDifficulty(t *testing.T) {
	server := &ProxyServer{config: &Config{}}
	server.config.Proxy.Difficulty = 2000000000
	server.config.Proxy.RandomX.Enabled = true
	server.config.Proxy.RandomX.ShareDifficulty = 5000

	got := targetHexToDiff(server.GetPoolShareTarget()).Int64()
	if got != 5000 {
		t.Fatalf("expected RandomX stratum target difficulty 5000, got %d", got)
	}
}

func TestGetPoolShareTargetDoesNotUseLegacyDifficultyForRandomX(t *testing.T) {
	server := &ProxyServer{config: &Config{}}
	server.config.Proxy.Difficulty = 2000000000
	server.config.Proxy.RandomX.Enabled = true

	got := targetHexToDiff(server.GetPoolShareTarget()).Int64()
	if got != DefaultRandomXShareDifficulty {
		t.Fatalf("expected default RandomX stratum target difficulty %d, got %d", DefaultRandomXShareDifficulty, got)
	}
}

func TestNonceBytesToHexPadsToEightBytes(t *testing.T) {
	got := nonceBytesToHex([]byte{0x12, 0x34})
	want := "0x0000000000001234"
	if got != want {
		t.Fatalf("expected padded nonce %s, got %s", want, got)
	}
}

func TestNonceBytesToHexKeepsLastEightBytes(t *testing.T) {
	got := nonceBytesToHex([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09})
	want := "0x0203040506070809"
	if got != want {
		t.Fatalf("expected truncated nonce %s, got %s", want, got)
	}
}

func TestRandomXHashMeetsTargetUsesExactTarget(t *testing.T) {
	if !randomXHashMeetsTarget([]byte{0x0f}, "0x10") {
		t.Fatalf("expected hash below target to be a block candidate")
	}
	if randomXHashMeetsTarget([]byte{0x11}, "0x10") {
		t.Fatalf("expected hash above target not to be a block candidate")
	}
}

func TestRandomXStratumTargetUsesCompactLittleEndianTarget(t *testing.T) {
	got := randomXStratumTarget(1000)
	want := "37894100"
	if got != want {
		t.Fatalf("expected compact little-endian target %s, got %s", want, got)
	}
}

func TestRandomXStratumTargetDoesNotPanicForLowDifficulty(t *testing.T) {
	got := randomXStratumTarget(1)
	want := "ffffffff"
	if got != want {
		t.Fatalf("expected maximum compact target %s, got %s", want, got)
	}
}

func TestFormatRandomXTargetUsesCompactLittleEndianTarget(t *testing.T) {
	got := formatRandomXTarget(1000)
	want := randomXStratumTarget(1000)
	if got != want {
		t.Fatalf("expected compact little-endian target %s, got %s", want, got)
	}
}

func TestFormatTargetUsesCompactLittleEndianTarget(t *testing.T) {
	got := formatTarget(1000)
	want := randomXStratumTarget(1000)
	if got != want {
		t.Fatalf("expected compact little-endian target %s, got %s", want, got)
	}
}
