package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const WalletFile = ".gochain-wallet.json"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
func printJSON(v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}
func flags(name string) *flag.FlagSet { return flag.NewFlagSet(name, flag.ContinueOnError) }
func publicWallet(w *Wallet) map[string]string {
	return map[string]string{"address": w.Address, "public_key": w.PublicKey}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "checkpoint":
		return runCheckpoint(args[1:])
	case "simulate":
		return runSimulation(args[1:])
	case "amm":
		return runAMM(args[1:])
	case "stratum":
		return runStratum(args[1:])
	case "merged-stratum":
		return runMergedStratum(args[1:])
	case "wallet":
		if len(args) < 2 {
			return fmt.Errorf("usage: gochain wallet create|address [--file PATH]")
		}
		fs := flags("wallet")
		path := fs.String("file", WalletFile, "wallet file")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		var w *Wallet
		var err error
		switch args[1] {
		case "create":
			w, err = CreateWallet(*path)
		case "address":
			w, err = LoadWallet(*path)
		default:
			return fmt.Errorf("unknown wallet command")
		}
		if err != nil {
			return err
		}
		return printJSON(publicWallet(w))
	case "serve":
		fs := flags("serve")
		port := fs.String("port", strings.TrimPrefix(DefaultNode, "http://127.0.0.1:"), "HTTP port")
		host := fs.String("host", "127.0.0.1", "listen address")
		auto := fs.Bool("mine", false, "automatically mine blocks")
		interval := fs.Duration("interval", time.Minute, "time between mining attempts")
		miner := fs.String("miner", "", "reward wallet address (defaults to local wallet)")
		wallet := fs.String("wallet", WalletFile, "local wallet file")
		data := fs.String("data", ChainFile, "ledger path")
		peers := fs.String("peers", "", "comma-separated loopback peer URLs")
		checkpointFile := fs.String("checkpoint", "", "trusted signed checkpoint file (checkpoint build only)")
		authority := fs.String("authority", "", "trusted checkpoint authority public key, from operator")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *interval < time.Second {
			return fmt.Errorf("mining interval must be at least 1s")
		}
		var checkpoint *Checkpoint
		if CheckpointRequired {
			var err error
			checkpoint, err = loadCheckpoint(*checkpointFile, *authority)
			if err != nil {
				return err
			}
		} else if *checkpointFile != "" || *authority != "" {
			return fmt.Errorf("use the separate -tags checkpoint build for trusted checkpoints")
		}
		if *miner == "" {
			w, err := LoadWallet(*wallet)
			if errors.Is(err, os.ErrNotExist) {
				w, err = CreateWallet(*wallet)
			}
			if err != nil {
				return err
			}
			*miner = w.Address
		}
		if !validAddress(*miner) {
			return fmt.Errorf("invalid mining wallet address")
		}
		release, err := acquireChainLock(*data)
		if err != nil {
			return err
		}
		defer release()
		bc, err := LoadOrCreate(*data)
		if err != nil {
			return err
		}
		bc.checkpoint = checkpoint
		if err := bc.checkCheckpoint(bc.state, true); err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		pm, err := newPeerManager(bc, strings.Split(*peers, ","))
		if err != nil {
			return err
		}
		return serve(ctx, bc, net.JoinHostPort(*host, *port), *miner, *auto, *interval, pm)
	case "mine":
		fs := flags("mine")
		miner := fs.String("miner", "", "reward wallet address")
		wallet := fs.String("wallet", WalletFile, "wallet file")
		node := fs.String("node", DefaultNode, "node URL")
		offline := fs.Bool("offline", false, "mine directly into local ledger with the server stopped")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *miner == "" {
			w, err := LoadWallet(*wallet)
			if err != nil {
				return err
			}
			*miner = w.Address
		}
		if !*offline {
			var b Block
			if err := request(*node, "/mine", map[string]string{"miner": *miner}, &b); err != nil {
				return err
			}
			return printJSON(b)
		}
		if CheckpointRequired {
			return fmt.Errorf("offline mining disabled in checkpoint build; use the node enforcing the trusted checkpoint")
		}
		release, err := acquireChainLock(ChainFile)
		if err != nil {
			return err
		}
		defer release()
		bc, err := LoadOrCreate(ChainFile)
		if err != nil {
			return err
		}
		b, err := bc.MinePending(*miner)
		if err != nil {
			return err
		}
		return printJSON(b)
	case "send":
		fs := flags("send")
		wallet := fs.String("wallet", WalletFile, "signing wallet file")
		to := fs.String("to", "", "recipient wallet address")
		amount := fs.String("amount", "0", "GO amount, up to 8 decimal places")
		memo := fs.String("memo", "", "transaction memo")
		node := fs.String("node", DefaultNode, "node URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		w, err := LoadWallet(*wallet)
		if err != nil {
			return err
		}
		var account Account
		if err := request(*node, "/balance/"+w.Address, nil, &account); err != nil {
			return err
		}
		atoms, err := parseAmount(*amount)
		if err != nil {
			return err
		}
		tx, err := w.Sign(*to, atoms, *memo, account.NextNonce)
		if err != nil {
			return err
		}
		var result any
		if err := request(*node, "/tx", tx, &result); err != nil {
			return err
		}
		return printJSON(result)
	case "balance":
		if len(args) != 2 {
			return fmt.Errorf("usage: gochain balance <wallet-address>")
		}
		var result Account
		if err := request(DefaultNode, "/balance/"+args[1], nil, &result); err != nil {
			return err
		}
		return printJSON(result)
	case "stats", "chain":
		var result any
		if err := request(DefaultNode, "/"+args[0], nil, &result); err != nil {
			return err
		}
		return printJSON(result)
	case "validate":
		fs := flags("validate")
		data := fs.String("data", ChainFile, "ledger path")
		checkpointFile := fs.String("checkpoint", "", "trusted signed checkpoint file")
		authority := fs.String("authority", "", "trusted authority public key")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var checkpoint *Checkpoint
		if CheckpointRequired {
			var err error
			checkpoint, err = loadCheckpoint(*checkpointFile, *authority)
			if err != nil {
				return err
			}
		} else if *checkpointFile != "" || *authority != "" {
			return fmt.Errorf("use the checkpoint build")
		}
		if _, err := os.Stat(*data); err != nil {
			return err
		}
		bc, err := LoadOrCreate(*data)
		if err != nil {
			return err
		}
		bc.checkpoint = checkpoint
		if err := bc.Validate(); err != nil {
			return err
		}
		fmt.Printf("Valid. %d blocks; %s GO minted.\n", bc.Stats().Blocks, bc.Stats().SupplyGO)
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// Keep one writer process per ledger; HTTP commands use the running node.
func acquireChainLock(path string) (func(), error) {
	lock := path + ".lock"
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot lock ledger; another node may be running (after a crash, verify it stopped before removing %s): %w", lock, err)
	}
	if _, err = fmt.Fprintf(f, "pid=%d\n", os.Getpid()); err != nil {
		f.Close()
		os.Remove(lock)
		return nil, err
	}
	if err = f.Close(); err != nil {
		os.Remove(lock)
		return nil, err
	}
	return func() { _ = os.Remove(lock) }, nil
}
func request(node, path string, body any, out any) error {
	var reader io.Reader
	method := http.MethodGet
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
		method = http.MethodPost
	}
	req, err := http.NewRequest(method, strings.TrimRight(node, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("node returned %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.NewDecoder(res.Body).Decode(out)
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("body must be a JSON object")
	}
	object := json.NewDecoder(bytes.NewReader(raw))
	object.DisallowUnknownFields()
	if err := object.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("body must contain exactly one JSON object")
	}
	return nil
}
func newHandler(bc *Blockchain, defaultMiner string, auto bool, interval time.Duration, managers ...*PeerManager) http.Handler {
	return newHandlerMonitored(bc, defaultMiner, auto, interval, nil, managers...)
}
func newHandlerMonitored(bc *Blockchain, defaultMiner string, auto bool, interval time.Duration, monitor *miningMonitor, managers ...*PeerManager) http.Handler {
	mux := http.NewServeMux()
	registerDashboard(mux)
	registerAMM(mux, bc)
	mux.HandleFunc("GET /explorer/blocks", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, explorerBlocks(bc)) })
	mux.HandleFunc("GET /sync", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, bc.Snapshot()) })
	mux.HandleFunc("GET /peers", func(w http.ResponseWriter, r *http.Request) {
		if len(managers) == 0 {
			writeJSON(w, []PeerStatus{})
			return
		}
		writeJSON(w, managers[0].Status())
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		var mining any
		if monitor != nil {
			mining = monitor.snapshot()
		}
		writeJSON(w, map[string]any{"server_time": time.Now().UTC(), "mining": mining, "status": "ok", "chain_id": ChainID, "automatic_mining": auto, "mining_interval": interval.String(), "miner": defaultMiner, "mining_mode": "scheduled_fixed_difficulty_experiment"})
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, bc.Stats()) })
	mux.HandleFunc("GET /chain", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, bc.Blocks()) })
	mux.HandleFunc("GET /balance/", func(w http.ResponseWriter, r *http.Request) {
		a, err := bc.Account(strings.TrimPrefix(r.URL.Path, "/balance/"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, a)
	})
	mux.HandleFunc("POST /tx", func(w http.ResponseWriter, r *http.Request) {
		var tx Transaction
		if err := decodeBody(w, r, &tx); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := bc.AddTx(tx); err != nil {
			status := 400
			var pe *os.PathError
			if errors.As(err, &pe) {
				status = 500
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, map[string]any{"queued": tx})
	})
	mux.HandleFunc("POST /mine", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Miner string `json:"miner"`
		}
		if err := decodeBody(w, r, &body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if body.Miner == "" {
			body.Miner = defaultMiner
		}
		if !validAddress(body.Miner) {
			http.Error(w, "invalid miner address", 400)
			return
		}
		b, err := bc.MinePending(body.Miner)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, b)
	})
	mux.HandleFunc("POST /block", func(w http.ResponseWriter, r *http.Request) {
		var b Block
		if err := decodeBody(w, r, &b); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		snapshot := bc.Snapshot()
		candidate := copyState(snapshot)
		candidate.Blocks = append(candidate.Blocks, &b)
		adopted, err := bc.Adopt(candidate)
		if err != nil {
			http.Error(w, fmt.Sprintf("block rejected: %v", err), 400)
			return
		}
		if !adopted {
			http.Error(w, "block did not extend active chain", 409)
			return
		}
		writeJSON(w, map[string]any{"status": "adopted", "index": b.Index, "hash": b.Hash})
	})
	return mux
}
func serve(ctx context.Context, bc *Blockchain, addr, miner string, auto bool, interval time.Duration, pm *PeerManager) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	monitor := &miningMonitor{}
	server := &http.Server{Handler: newHandlerMonitored(bc, miner, auto, interval, monitor, pm), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 30 * time.Second}
	peerDone := make(chan struct{})
	pm.SyncOnce(ctx)
	go pm.Run(ctx, peerDone)
	done := make(chan struct{})
	if auto {
		go mineLoopMonitored(ctx, bc, miner, interval, monitor, done)
	} else {
		close(done)
	}
	go func() {
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("GoChain listening at http://%s; miner=%s; automatic mining=%t (%s)", addr, miner, auto, interval)
	err = server.Serve(listener)
	cancel()
	<-done
	<-peerDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func usage() {
	fmt.Println(`GoChain — local proof-of-work chain, ticker GO.
  gochain wallet create [--file PATH]
  gochain wallet address [--file PATH]
  gochain serve --port 8080 --mine --interval 1m [--data PATH] [--peers URL,...]
  gochain mine [--miner ADDRESS] [--offline]
  gochain send --to ADDRESS --amount 1 [--wallet PATH] [--memo TEXT]
  gochain stratum --port 3333 [--diff 16] [--node http://127.0.0.1:8086]
  gochain merged-stratum --port 3335 [--dash 3336] [--btc-bits BITS] [--node http://127.0.0.1:8092]
  gochain balance ADDRESS
  gochain stats | chain | validate

Mining starts at 10 GO per block; rewards halve each February 29 from 2028. Transfers require wallet signatures.
No premine, fees, or fixed supply cap. Default server binds to localhost.`)
}
