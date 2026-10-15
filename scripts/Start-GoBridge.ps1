param([int]$NodePort = 8086, [int]$Port = 5180, [string]$SolanaRpc = 'http://127.0.0.1:8897', [switch]$NoBrowser)
$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$app = Join-Path $repo 'solana-devnet'
$runtime = Join-Path $app '.local\bridge-node'
$bridge = Join-Path $app '.local\bridge'
if ($NodePort -lt 1024 -or $NodePort -gt 65535 -or $Port -lt 1024 -or $Port -gt 65535 -or $NodePort -eq $Port) { throw 'Choose distinct unprivileged ports' }
if ($SolanaRpc -notmatch '^http://127\.0\.0\.1:\d+$') { throw 'Use a local Solana test validator RPC' }
if (!(Test-Path (Join-Path $app 'node_modules\tsx\dist\cli.mjs'))) { throw 'Run npm ci in solana-devnet first' }
$nodeExe = (Get-Command node -ErrorAction Stop).Source
$cli = Join-Path $app 'node_modules\tsx\dist\cli.mjs'
New-Item -ItemType Directory -Force $runtime | Out-Null
$binary = Join-Path $runtime 'gochain-checkpoint.exe'
if (!(Test-Path $binary)) {
  $goExe = (Get-Command go -ErrorAction Stop).Source
  Push-Location $repo
  try { & $goExe build -tags checkpoint -o $binary .; if ($LASTEXITCODE) { throw 'Checkpoint build failed' } }
  finally { Pop-Location }
}
$authorityFile = Join-Path $runtime 'authority.wallet.json'
$minerFile = Join-Path $runtime 'miner.wallet.json'
$checkpointFile = Join-Path $runtime 'checkpoint.json'
$ledger = Join-Path $runtime 'chain.json'
foreach ($file in @($authorityFile, $minerFile)) {
  if (!(Test-Path $file)) { & $binary wallet create --file $file | Out-Null; if ($LASTEXITCODE) { throw 'Wallet creation failed' } }
}
if (!(Test-Path $checkpointFile)) {
  if (Test-Path $ledger) { throw 'Existing ledger requires an inspected signed checkpoint; refusing to reset' }
  & $binary checkpoint sign --genesis --wallet $authorityFile --out $checkpointFile | Out-Null
  if ($LASTEXITCODE) { throw 'Checkpoint signing failed' }
}
$authority = (& $binary wallet address --file $authorityFile | ConvertFrom-Json).public_key
$listener = Get-NetTCPConnection -LocalPort $NodePort -State Listen -ErrorAction SilentlyContinue
if ($listener) {
  $owner = Get-CimInstance Win32_Process -Filter "ProcessId=$($listener[0].OwningProcess)"
  if (!$owner.CommandLine.Contains($ledger)) { throw "Port $NodePort belongs to another node. Choose another port; no process was stopped." }
} else {
  $arguments = @('serve','--port',"$NodePort",'--data',('"'+$ledger+'"'),'--wallet',('"'+$minerFile+'"'),'--checkpoint',('"'+$checkpointFile+'"'),'--authority',$authority)
  Start-Process -FilePath $binary -ArgumentList $arguments -WorkingDirectory $repo -WindowStyle Hidden -RedirectStandardOutput (Join-Path $runtime 'node.stdout.log') -RedirectStandardError (Join-Path $runtime 'node.stderr.log') | Out-Null
}
$nodeUrl = "http://127.0.0.1:$NodePort"
$stats = $null
for ($attempt = 0; $attempt -lt 30; $attempt++) {
  try { $stats = Invoke-RestMethod "$nodeUrl/stats" -TimeoutSec 2; break } catch { Start-Sleep -Milliseconds 200 }
}
if (!$stats -or $stats.chain_id -ne 'gochain-checkpoint-amm-test-v1' -or !$stats.valid -or !$stats.checkpoint_ready) { throw 'Checkpoint node is not ready. Inspect logs/checkpoint expiry; no ledger was reset. See BRIDGE.md for renewal.' }
Push-Location $app
try {
  if (!(Test-Path (Join-Path $bridge 'config.json'))) {
    & $nodeExe $cli 'scripts/bridge.ts' 'init' $nodeUrl $SolanaRpc
    if ($LASTEXITCODE) { throw 'Bridge initialization failed. Preserve keys/journal and inspect the error; do not delete and retry.' }
  } else {
    $config = Get-Content (Join-Path $bridge 'config.json') -Raw | ConvertFrom-Json
    if ($config.node -ne $nodeUrl -or $config.rpc -ne $SolanaRpc) { throw 'Existing bridge endpoint mismatch; preserve its original networks' }
    & $nodeExe $cli 'scripts/bridge.ts' 'status'
    if ($LASTEXITCODE) { throw 'Existing bridge failed its backing/network checks' }
  }
  $existingUi = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
  if ($existingUi) {
    $page = Invoke-WebRequest "http://127.0.0.1:$Port/" -UseBasicParsing -TimeoutSec 5
    if ($page.Content -notlike '*Mined GO. Wrapped on Solana.*') { throw "Port $Port is occupied by another service" }
  } else {
    Start-Process -FilePath $nodeExe -ArgumentList @(('"'+$cli+'"'),'scripts/bridge-server.ts','--port',"$Port") -WorkingDirectory $app -WindowStyle Hidden -RedirectStandardOutput (Join-Path $runtime 'ui.stdout.log') -RedirectStandardError (Join-Path $runtime 'ui.stderr.log') | Out-Null
    $ready = $false
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
      try { $page = Invoke-WebRequest "http://127.0.0.1:$Port/" -UseBasicParsing -TimeoutSec 2; $ready = $page.StatusCode -eq 200; if ($ready) { break } } catch { }
      Start-Sleep -Milliseconds 200
    }
    if (!$ready) { throw 'Bridge UI did not become ready. Inspect .local/bridge-node/ui.stderr.log' }
  }
} finally { Pop-Location }
Write-Host "Local trusted bridge ready: http://127.0.0.1:$Port/"
Write-Host 'Manual test mining only. Dedicated server-held wallets; no mainnet, no existing-wallet migration.'
if (!$NoBrowser) { Start-Process "http://127.0.0.1:$Port/" }
