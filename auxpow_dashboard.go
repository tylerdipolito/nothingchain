package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"sync/atomic"
	"time"
)

type shareRecord struct {
	timestamp time.Time
	diff      float64
}

type minedBlockSummary struct {
	Index       int    `json:"index"`
	Hash        string `json:"hash"`
	ParentNonce string `json:"parent_nonce"`
	ParentHash  string `json:"parent_hash"`
	Miner       string `json:"miner"`
	RewardAtoms int64  `json:"reward_atoms"`
	Timestamp   int64  `json:"timestamp"`
}

func (s *AuxPoWStratumServer) recordShare(diff float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shareHistory = append(s.shareHistory, shareRecord{timestamp: time.Now(), diff: diff})
	if len(s.shareHistory) > 5000 {
		s.shareHistory = s.shareHistory[len(s.shareHistory)-2000:]
	}
}

func (s *AuxPoWStratumServer) recordMinedBlock(ab *AuxBlock, parentNonce uint32, parentHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.minedBlocks = append([]minedBlockSummary{{
		Index:       ab.Index,
		Hash:        ab.Hash,
		ParentNonce: fmt.Sprintf("0x%08x", parentNonce),
		ParentHash:  parentHash,
		Miner:       ab.Miner,
		RewardAtoms: ab.Reward,
		Timestamp:   time.Now().Unix(),
	}}, s.minedBlocks...)
	if len(s.minedBlocks) > 30 {
		s.minedBlocks = s.minedBlocks[:30]
	}
}

func (s *AuxPoWStratumServer) HashrateGHs(window time.Duration) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-window)
	var totalDiff float64
	count := 0
	for i := len(s.shareHistory) - 1; i >= 0; i-- {
		if s.shareHistory[i].timestamp.Before(cutoff) {
			break
		}
		totalDiff += s.shareHistory[i].diff
		count++
	}
	if count == 0 {
		return 0
	}
	hashesPerSec := (totalDiff * 4294967296.0) / window.Seconds()
	return hashesPerSec / 1e9
}

func (s *AuxPoWStratumServer) StartDashboard(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(stratumDashboardHTML))
	})

	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var balanceAtoms int64
		if s.GoChainNodeURL != "" && s.DefaultMiner != "" {
			var acct Account
			if err := request(s.GoChainNodeURL, "/balance/"+s.DefaultMiner, nil, &acct); err == nil {
				balanceAtoms = acct.Balance
			}
		}

		s.mu.RLock()
		var activeWorker string
		var remoteAddr string
		for _, sess := range s.sessions {
			if sess.Authorized {
				activeWorker = sess.Worker
				remoteAddr = sess.Conn.RemoteAddr().String()
				break
			}
		}
		recentBlocksCopy := append([]minedBlockSummary{}, s.minedBlocks...)
		currentHeight := 0
		if s.currentJob != nil && s.currentJob.CandidateBlock != nil {
			currentHeight = s.currentJob.CandidateBlock.Index
		}
		s.mu.RUnlock()

		diff1 := BitsToTarget(0x1d00ffff)
		netTarget := BitsToTarget(s.GoTargetBits)
		netDiffFloat, _ := new(big.Float).Quo(new(big.Float).SetInt(diff1), new(big.Float).SetInt(netTarget)).Float64()

		stats := map[string]any{
			"mining":              activeWorker != "",
			"worker":              activeWorker,
			"miner_address":       s.DefaultMiner,
			"remote_addr":         remoteAddr,
			"hashrate_ghs":        s.HashrateGHs(60 * time.Second),
			"accepted_shares":     atomic.LoadUint64(&s.AcceptedShares),
			"rejected_shares":     atomic.LoadUint64(&s.RejectedShares),
			"share_difficulty":    s.ShareDiff,
			"network_target_bits": fmt.Sprintf("0x%08x", s.GoTargetBits),
			"network_difficulty":  netDiffFloat,
			"mined_blocks_count":  atomic.LoadUint64(&s.MinedBlocks),
			"current_height":      currentHeight,
			"miner_balance_atoms": balanceAtoms,
			"miner_balance_go":    fmt.Sprintf("%.8f GO", float64(balanceAtoms)/1e8),
			"recent_blocks":       recentBlocksCopy,
			"solana_bridge": map[string]any{
				"solana_program_id": "63BJKNhEb4ZAnzHHyuvhFXZT3XGuwEyrojzQa2yTCTDy",
				"wgo_mint":          "4G3S3qF6TUwe9dFisfJjRxUgG3mhzRgC1PHTTV1wVdAq",
				"goamm_pool_pda":    "DVxjjoFiCJ7mSQ1PNEHWBJiJX6sHagU8hPtnekxpht1X",
				"pool_fee":          "30 bps (0.30%)",
				"status":            "VERIFIED_ON_DEVNET",
			},
		}
		_ = json.NewEncoder(w).Encode(stats)
	})

	server := &http.Server{Addr: addr, Handler: mux}
	log.Printf("[DASHBOARD] 📊 Web Dashboard live at http://%s", addr)
	return server.ListenAndServe()
}

const stratumDashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>GoChain Bitaxe AuxPoW Live Mining Console</title>
  <style>
    :root {
      --bg: #0b0f19;
      --card-bg: #111827;
      --border: #1f2937;
      --accent: #10b981;
      --accent-glow: rgba(16, 185, 129, 0.2);
      --text: #f3f4f6;
      --text-muted: #9ca3af;
      --font-mono: 'SF Mono', Consolas, 'Liberation Mono', Menlo, monospace;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      background: var(--bg);
      color: var(--text);
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
      padding: 24px;
      line-height: 1.5;
    }
    .header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 24px;
      padding-bottom: 16px;
      border-bottom: 1px solid var(--border);
    }
    .title-group { display: flex; align-items: center; gap: 12px; }
    .title-group h1 { font-size: 20px; font-weight: 700; letter-spacing: -0.5px; }
    .badge {
      font-family: var(--font-mono);
      font-size: 11px;
      padding: 4px 8px;
      border-radius: 4px;
      text-transform: uppercase;
      font-weight: 600;
    }
    .badge-online { background: #064e3b; color: #34d399; border: 1px solid #059669; }
    .badge-idle { background: #374151; color: #9ca3af; }
    .pulse {
      display: inline-block;
      width: 8px;
      height: 8px;
      border-radius: 50%;
      background: #10b981;
      margin-right: 6px;
      box-shadow: 0 0 8px #10b981;
      animation: pulse 2s infinite;
    }
    @keyframes pulse { 0% { opacity: 1; } 50% { opacity: 0.3; } 100% { opacity: 1; } }
    .grid-kpi {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
      gap: 16px;
      margin-bottom: 24px;
    }
    .card {
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 20px;
    }
    .card-title {
      font-size: 12px;
      color: var(--text-muted);
      text-transform: uppercase;
      letter-spacing: 0.5px;
      margin-bottom: 8px;
      font-weight: 600;
    }
    .card-val {
      font-size: 28px;
      font-weight: 700;
      font-family: var(--font-mono);
      color: #fff;
    }
    .card-sub { font-size: 12px; color: var(--text-muted); margin-top: 4px; font-family: var(--font-mono); }
    .text-green { color: var(--accent) !important; }
    .grid-details {
      display: grid;
      grid-template-columns: 2fr 1fr;
      gap: 16px;
      margin-bottom: 24px;
    }
    @media (max-width: 900px) { .grid-details { grid-template-columns: 1fr; } }
    .table-container { overflow-x: auto; }
    table { width: 100%; border-collapse: collapse; font-size: 13px; font-family: var(--font-mono); }
    th { text-align: left; padding: 10px 12px; color: var(--text-muted); border-bottom: 1px solid var(--border); font-size: 11px; text-transform: uppercase; }
    td { padding: 10px 12px; border-bottom: 1px solid #1a2234; }
    .solana-card {
      background: linear-gradient(145deg, #131b2e 0%, #161b26 100%);
      border: 1px solid #2d3748;
    }
    .link-solana { color: #818cf8; text-decoration: none; word-break: break-all; }
    .link-solana:hover { text-decoration: underline; }
  </style>
</head>
<body>
  <div class="header">
    <div class="title-group">
      <h1>GoChain Bitaxe AuxPoW Console</h1>
      <span id="badge-status" class="badge badge-idle">CONNECTING...</span>
    </div>
    <div style="font-family: var(--font-mono); font-size: 12px; color: var(--text-muted);">
      Stratum Port: 3333 | Dashboard: 3334
    </div>
  </div>

  <div class="grid-kpi">
    <div class="card">
      <div class="card-title">ASIC Hashrate (60s)</div>
      <div class="card-val text-green" id="val-hashrate">0.0 GH/s</div>
      <div class="card-sub" id="val-worker">Worker: None</div>
    </div>
    <div class="card">
      <div class="card-title">Accepted Shares</div>
      <div class="card-val" id="val-shares">0</div>
      <div class="card-sub" id="val-rejected">Rejected: 0 | Diff: 16.0</div>
    </div>
    <div class="card">
      <div class="card-title">GoChain Blocks Mined</div>
      <div class="card-val text-green" id="val-blocks">0</div>
      <div class="card-sub" id="val-height">Height: 0</div>
    </div>
    <div class="card">
      <div class="card-title">Miner Balance</div>
      <div class="card-val" id="val-balance">0.00 GO</div>
      <div class="card-sub" id="val-atoms">0 atoms</div>
    </div>
  </div>

  <div class="grid-details">
    <div class="card">
      <div class="card-title" style="margin-bottom: 16px;">Recent AuxPoW Blocks Mined by Bitaxe</div>
      <div class="table-container">
        <table>
          <thead>
            <tr>
              <th>Height</th>
              <th>GoChain Hash</th>
              <th>Bitcoin Parent Nonce</th>
              <th>Reward</th>
              <th>Time</th>
            </tr>
          </thead>
          <tbody id="table-blocks">
            <tr><td colspan="5" style="text-align:center; color: var(--text-muted); padding: 24px;">Waiting for first ASIC-mined block...</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="card solana-card">
      <div class="card-title" style="color: #a5b4fc;">Solana Liquidity Bridge (GoAMM)</div>
      <p style="font-size: 13px; color: var(--text-muted); margin-bottom: 12px;">
        Native GO mined by your Bitaxe can be locked on GoChain and minted as 8-decimal wGO on Solana Devnet to trade on GoAMM!
      </p>
      <div style="font-family: var(--font-mono); font-size: 12px; margin-bottom: 8px;">
        <span style="color: var(--text-muted);">Status:</span> <span class="text-green" id="bridge-status">ACTIVE</span>
      </div>
      <div style="font-family: var(--font-mono); font-size: 11px; margin-bottom: 8px;">
        <span style="color: var(--text-muted);">wGO Mint:</span><br>
        <a class="link-solana" target="_blank" href="https://explorer.solana.com/address/4G3S3qF6TUwe9dFisfJjRxUgG3mhzRgC1PHTTV1wVdAq?cluster=devnet">4G3S3qF6TUwe9dFisfJjRxUgG3mhzRgC1PHTTV1wVdAq</a>
      </div>
      <div style="font-family: var(--font-mono); font-size: 11px; margin-bottom: 8px;">
        <span style="color: var(--text-muted);">GoAMM Pool PDA:</span><br>
        <a class="link-solana" target="_blank" href="https://explorer.solana.com/address/DVxjjoFiCJ7mSQ1PNEHWBJiJX6sHagU8hPtnekxpht1X?cluster=devnet">DVxjjoFiCJ7mSQ1PNEHWBJiJX6sHagU8hPtnekxpht1X</a>
      </div>
      <div style="font-family: var(--font-mono); font-size: 11px;">
        <span style="color: var(--text-muted);">GoAMM Fee:</span> 30 bps (0.30%) CPMM
      </div>
    </div>
  </div>

  <script>
    async function update() {
      try {
        const res = await fetch('/api/stats');
        const data = await res.json();

        // Update badge
        const badge = document.getElementById('badge-status');
        if (data.mining) {
          badge.className = 'badge badge-online';
          badge.innerHTML = '<span class="pulse"></span>BITAXE MINING';
        } else {
          badge.className = 'badge badge-idle';
          badge.innerText = 'WAITING FOR BITAXE';
        }

        // KPIs
        document.getElementById('val-hashrate').innerText = (data.hashrate_ghs > 0 ? data.hashrate_ghs.toFixed(1) : '0.0') + ' GH/s';
        document.getElementById('val-worker').innerText = data.worker ? ('Worker: ' + data.worker) : 'Worker: None';
        document.getElementById('val-shares').innerText = data.accepted_shares;
        document.getElementById('val-rejected').innerText = 'Rejected: ' + data.rejected_shares + ' | Diff: ' + data.share_difficulty;
        document.getElementById('val-blocks').innerText = data.mined_blocks_count;
        document.getElementById('val-height').innerText = 'Chain Height: ' + data.current_height;
        document.getElementById('val-balance').innerText = data.miner_balance_go;
        document.getElementById('val-atoms').innerText = data.miner_balance_atoms.toLocaleString() + ' atoms';

        // Table
        const tbody = document.getElementById('table-blocks');
        if (data.recent_blocks && data.recent_blocks.length > 0) {
          tbody.innerHTML = data.recent_blocks.map(function(b) {
            return '<tr>' +
              '<td><strong>#' + b.index + '</strong></td>' +
              '<td title="' + b.hash + '">' + b.hash.substring(0, 12) + '...</td>' +
              '<td style="color:#f59e0b;">' + b.parent_nonce + '</td>' +
              '<td class="text-green">' + (b.reward_atoms / 1e8).toFixed(2) + ' GO</td>' +
              '<td style="color:var(--text-muted);">' + new Date(b.timestamp * 1000).toLocaleTimeString() + '</td>' +
            '</tr>';
          }).join('');
        }
      } catch (err) {
        console.error('Failed to poll /api/stats', err);
      }
    }
    setInterval(update, 1500);
    update();
  </script>
</body>
</html>
`
