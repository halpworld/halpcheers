package pow_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/internal/auth/pow"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/obs"
)

func TestCheckPoWCorrectness(t *testing.T) {
	handle := "e7k4p2m9qx3v"
	epoch := uint64(123456)
	var challenge [32]byte
	challenge[0] = 0xAA
	challenge[31] = 0x55

	difficulty := uint8(10) // Small difficulty for fast test
	nonce, ok := pow.SolvePoW(handle, epoch, challenge, difficulty, 1_000_000)
	if !ok {
		t.Fatalf("failed to solve PoW within 1,000,000 iterations")
	}

	if !pow.CheckPoW(handle, epoch, challenge, nonce, difficulty) {
		t.Fatalf("CheckPoW returned false for solved nonce %s", nonce)
	}

	// Tampered handle must fail
	if pow.CheckPoW("e99999999999", epoch, challenge, nonce, difficulty) {
		t.Fatalf("CheckPoW accepted tampered handle")
	}

	// Tampered epoch must fail
	if pow.CheckPoW(handle, epoch+1, challenge, nonce, difficulty) {
		t.Fatalf("CheckPoW accepted tampered epoch")
	}

	// Tampered challenge must fail
	challenge[0] ^= 0xFF
	if pow.CheckPoW(handle, epoch, challenge, nonce, difficulty) {
		t.Fatalf("CheckPoW accepted tampered challenge")
	}
}

func TestEpochToleranceAndExpiration(t *testing.T) {
	metrics := obs.NewMetrics()
	engine, err := pow.New(pow.Config{
		EpochDuration: 5 * time.Minute,
		FloorMs:       10,
		Metrics:       metrics,
	})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	handle := "e7k4p2m9qx3v"
	target := "e7k4p2m9qx3v"
	sender := core.AccountID(1234567812345678)

	curEpoch := engine.CurrentEpoch()
	diff := engine.Difficulty(target, sender)

	// Solve for current epoch
	_, curChal, _ := engine.CurrentChallenge()
	nonceCur, ok := pow.SolvePoW(handle, curEpoch, curChal, diff, 1_000_000)
	if !ok {
		t.Fatalf("failed to solve for current epoch")
	}
	tokenCur := fmt.Sprintf("%d.%s", curEpoch, nonceCur)
	if err := engine.Verify(handle, tokenCur, target, sender); err != nil {
		t.Fatalf("expected current epoch to be accepted, got: %v", err)
	}

	// Adjacent epoch cur-1 should be accepted (clock skew tolerance)
	if curEpoch > 0 {
		prevEpoch := curEpoch - 1
		tokenPrev := fmt.Sprintf("%d.dummy", prevEpoch)
		err := engine.Verify(handle, tokenPrev, target, sender)
		// Should fail with ErrInsufficient (since dummy nonce), but NOT ErrExpiredEpoch
		if err == pow.ErrExpiredEpoch {
			t.Fatalf("expected adjacent epoch cur-1 to be within tolerance, got ErrExpiredEpoch")
		}
	}

	// Adjacent epoch cur+1 should be accepted
	nextEpoch := curEpoch + 1
	tokenNext := fmt.Sprintf("%d.dummy", nextEpoch)
	err = engine.Verify(handle, tokenNext, target, sender)
	if err == pow.ErrExpiredEpoch {
		t.Fatalf("expected adjacent epoch cur+1 to be within tolerance, got ErrExpiredEpoch")
	}

	// Expired epoch cur-2 must be rejected with ErrExpiredEpoch
	if curEpoch >= 2 {
		oldEpoch := curEpoch - 2
		tokenOld := fmt.Sprintf("%d.dummy", oldEpoch)
		if err := engine.Verify(handle, tokenOld, target, sender); err != pow.ErrExpiredEpoch {
			t.Fatalf("expected expired epoch cur-2 to return ErrExpiredEpoch, got: %v", err)
		}
	}

	// Future epoch cur+2 must be rejected with ErrExpiredEpoch
	futureEpoch := curEpoch + 2
	tokenFuture := fmt.Sprintf("%d.dummy", futureEpoch)
	if err := engine.Verify(handle, tokenFuture, target, sender); err != pow.ErrExpiredEpoch {
		t.Fatalf("expected far future epoch cur+2 to return ErrExpiredEpoch, got: %v", err)
	}
}

func TestDifficultyEscalationAndDecay(t *testing.T) {
	metrics := obs.NewMetrics()
	feed := pow.NewDynamicFeed(14)

	engine, err := pow.New(pow.Config{
		EpochDuration: 5 * time.Minute,
		FloorMs:       10, // d = 14
		Feed:          feed,
		Metrics:       metrics,
	})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	target := "espikehandle"
	senderNormal := core.AccountID(1111222233334444)
	senderAbusive := core.AccountID(9999888877776666)

	// Base difficulty
	if d := engine.Difficulty("normalhandle", senderNormal); d != 14 {
		t.Fatalf("expected base difficulty 14, got %d", d)
	}

	// Target escalation (inbound EWMA spike)
	feed.SetTargetDifficulty(target, 18)
	if d := engine.Difficulty(target, senderNormal); d != 18 {
		t.Fatalf("expected escalated target difficulty 18, got %d", d)
	}
	// Normal handle should remain at base floor
	if d := engine.Difficulty("normalhandle", senderNormal); d != 14 {
		t.Fatalf("normal target should still be 14, got %d", d)
	}

	// Sender escalation (sender behaving oddly)
	feed.SetSenderDifficulty(senderAbusive, 20)
	if d := engine.Difficulty("normalhandle", senderAbusive); d != 20 {
		t.Fatalf("expected escalated sender difficulty 20, got %d", d)
	}

	// Decay back to normal
	feed.SetTargetDifficulty(target, 14)
	if d := engine.Difficulty(target, senderNormal); d != 14 {
		t.Fatalf("expected decayed difficulty 14, got %d", d)
	}
}

func TestSignupDifficulty(t *testing.T) {
	engine, err := pow.New(pow.Config{
		SignupMs: 1500,
	})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	// pow.signup_ms = 1500 maps to difficulty 21
	if d := engine.SignupDifficulty(); d != 21 {
		t.Fatalf("expected signup difficulty 21, got %d", d)
	}
}

func BenchmarkVerify(b *testing.B) {
	engine, err := pow.New(pow.Config{
		FloorMs: 10,
	})
	if err != nil {
		b.Fatalf("failed to create engine: %v", err)
	}

	handle := "e7k4p2m9qx3v"
	target := "e7k4p2m9qx3v"
	sender := core.AccountID(1234567812345678)

	curEpoch, curChal, diff := engine.CurrentChallenge()
	nonce, ok := pow.SolvePoW(handle, curEpoch, curChal, diff, 10_000_000)
	if !ok {
		b.Fatalf("failed to solve PoW for benchmark")
	}

	token := fmt.Sprintf("%d.%s", curEpoch, nonce)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := engine.Verify(handle, token, target, sender); err != nil {
			b.Fatalf("Verify failed: %v", err)
		}
	}
}
