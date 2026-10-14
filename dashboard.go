package main

import (
	"embed"
	"net/http"
	"strconv"
)

// Only these public files are embedded; wallet and ledger files are never served.
//go:embed web/index.html web/styles.css web/app.js
var dashboardFiles embed.FS

func registerDashboard(mux *http.ServeMux) {
	for _, asset := range []struct{ route, path, contentType string }{
		{"GET /{$}", "web/index.html", "text/html; charset=utf-8"},
		{"GET /styles.css", "web/styles.css", "text/css; charset=utf-8"},
		{"GET /app.js", "web/app.js", "text/javascript; charset=utf-8"},
	} {
		data, err := dashboardFiles.ReadFile(asset.path)
		if err != nil {
			panic(err)
		}
		mux.HandleFunc(asset.route, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", asset.contentType)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
			_, _ = w.Write(data)
		})
	}
}

// Explicit display projections preserve integer precision in browsers.
type explorerTx struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Amount string `json:"amount"`
	Memo   string `json:"memo"`
	Nonce  string `json:"nonce"`
}
type explorerBlock struct {
	Index     int          `json:"index"`
	Timestamp int64        `json:"timestamp"`
	Txs       []explorerTx `json:"txs"`
	PrevHash  string       `json:"prev_hash"`
	Nonce     string       `json:"nonce"`
	Miner     string       `json:"miner"`
	Reward    string       `json:"reward"`
	Hash      string       `json:"hash"`
}

func explorerBlocks(bc *Blockchain) any {
	blocks := bc.Blocks()
	out := make([]explorerBlock, len(blocks))
	for i, b := range blocks {
		txs := make([]explorerTx, len(b.Txs))
		for j, tx := range b.Txs {
			txs[j] = explorerTx{tx.From, tx.To, formatAmount(tx.Amount), tx.Memo, strconv.FormatUint(tx.Nonce, 10)}
		}
		out[i] = explorerBlock{b.Index, b.Timestamp, txs, b.PrevHash, strconv.FormatInt(b.Nonce, 10), b.Miner, formatAmount(b.Reward), b.Hash}
	}
	return struct {
		Tip    string          `json:"tip_hash"`
		Blocks []explorerBlock `json:"blocks"`
	}{blocks[len(blocks)-1].Hash, out}
}
