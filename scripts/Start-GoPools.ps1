[CmdletBinding()]
param(
    [switch]$CheckOnly,
    [switch]$NoBrowser,
    [switch]$StartValidator,
    [ValidateRange(1024, 65480)][int]$Port = 5174,
    [string]$ValidatorLedger,
    [string]$WslDistribution = 'Ubuntu'
)
$ErrorActionPreference = 'Stop'
$app = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\solana-devnet'))
$deploymentFile = Join-Path $app 'public\deployment.json'
if (-not (Test-Path -LiteralPath $deploymentFile)) {
    throw 'No deployment.json. Complete the local setup in solana-devnet/README.md first. This launcher never resets or initializes a ledger.'
}
$deployment = Get-Content -LiteralPath $deploymentFile -Raw | ConvertFrom-Json
$rpcUri = [Uri]$deployment.rpc
if ($deployment.network -ne 'localnet' -or $rpcUri.Scheme -ne 'http' -or $rpcUri.Host -ne '127.0.0.1' -or $rpcUri.Port -lt 1024 -or $rpcUri.Port -gt 65480 -or $rpcUri.AbsolutePath -ne '/' -or $rpcUri.Query -or $rpcUri.Fragment -or $rpcUri.UserInfo) {
    throw 'This launcher requires a loopback localnet RPC URL with an explicit unprivileged port.'
}
$rpcPort = $rpcUri.Port
function Rpc([string]$method, [object[]]$arguments = @()) {
    $body = @{jsonrpc='2.0'; id=1; method=$method; params=$arguments} | ConvertTo-Json -Depth 8 -Compress
    $answer = Invoke-RestMethod -Uri $deployment.rpc -Method Post -ContentType 'application/json' -Body $body -TimeoutSec 5
    if ($answer.error) { throw "RPC $method failed: $($answer.error.message)" }
    return $answer.result
}
function Account([string]$address) {
    return (Rpc 'getAccountInfo' @($address, @{encoding='base64'; commitment='confirmed'})).value
}
$runtime = Join-Path $app '.local'
New-Item -ItemType Directory -Path $runtime -Force | Out-Null
try { $health = Rpc 'getHealth' } catch {
    if ($CheckOnly -or -not $StartValidator) {
        throw 'Local validator is unavailable. Start the existing validator, or use -StartValidator -ValidatorLedger /absolute/existing/wsl/ledger. No ledger has been changed.'
    }
    if (Get-NetTCPConnection -LocalPort $rpcPort -State Listen -ErrorAction SilentlyContinue) {
        throw "Port $rpcPort has a listener but RPC is unhealthy. Refusing to start a competing validator."
    }
    if ($ValidatorLedger -notmatch '^/[A-Za-z0-9_./-]+$' -or $ValidatorLedger -match '(^|/)\.\.(/|$)' -or $WslDistribution -notmatch '^[A-Za-z0-9_.-]+$') {
        throw 'Supply an absolute WSL ledger path without spaces or traversal, and a valid WSL distribution name.'
    }
    # Require an existing ledger: creating a new one would disconnect the saved
    # deployment from its accounts. Never pass --reset or deploy automatically.
    & wsl.exe -d $WslDistribution -- test -f "$ValidatorLedger/genesis.bin"
    if ($LASTEXITCODE -ne 0) { throw 'Existing validator genesis.bin not found. Refusing to create a replacement ledger.' }
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    # Values are restricted above to path-safe characters or bounded integers.
    # Avoid nested positional-parameter quoting across Windows -> WSL -> bash.
    $validatorCommand = "exec solana-test-validator --ledger $ValidatorLedger --rpc-port $rpcPort --faucet-port $($rpcPort + 10) --gossip-port $($rpcPort + 20) --dynamic-port-range $($rpcPort + 20)-$($rpcPort + 50) --bind-address 127.0.0.1 --quiet"
    $validatorArgs = @('-d', $WslDistribution, '--', 'bash', '-lc', ('"' + $validatorCommand + '"'))
    $validator = Start-Process -FilePath 'wsl.exe' -ArgumentList $validatorArgs -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput (Join-Path $runtime "validator-$stamp.log") `
        -RedirectStandardError (Join-Path $runtime "validator-$stamp-error.log")
    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    do {
        try { $health = Rpc 'getHealth'; break } catch {
            $validator.Refresh()
            if ($validator.HasExited) { throw "Validator exited. Inspect .local/validator-$stamp-error.log; no restart attempted." }
            Start-Sleep -Milliseconds 500
        }
    } while ([DateTime]::UtcNow -lt $deadline)
}
if ($health -ne 'ok') { throw 'Validator did not become healthy. Inspect logs; no automatic restart attempted.' }
$genesis = Rpc 'getGenesisHash'
if ($genesis -in @('EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG', '5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d', '4uhcVJyU9pJkvQyS88uRDiswHXSCkY3zQawwpjk2NsNY')) {
    throw 'RPC belongs to a public Solana cluster. Refusing local startup.'
}
$program = Account $deployment.program
if (-not $program.executable) { throw 'Configured AMM program is missing or not executable. Restore the correct ledger.' }
$pool = Account $deployment.pool
if (-not $pool -or $pool.owner -ne $deployment.program) { throw 'Pool is missing or belongs to another program. Restore the correct ledger.' }
foreach ($address in @($deployment.mintA, $deployment.mintB, $deployment.vaultA, $deployment.vaultB, $deployment.lpMint, $deployment.lockedLP)) {
    $account = Account $address
    if (-not $account -or $account.owner -ne 'TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA') { throw "Missing or unexpected token account: $address" }
}
Write-Host "Local validator healthy. Genesis: $genesis"
Write-Host "AMM program and pool accounts found: $($deployment.pool)"
Write-Host 'Demo GO is separate from mined GoChain GO. No bridge is installed.'
$url = "http://127.0.0.1:$Port/"
$running = $false
try {
    $served = Invoke-RestMethod -Uri ($url + 'deployment.json') -TimeoutSec 3
    $running = $served.pool -eq $deployment.pool -and $served.program -eq $deployment.program -and $served.rpc -eq $deployment.rpc -and $served.network -eq 'localnet'
} catch { }
if (-not $running) {
    if ($CheckOnly) { throw "UI is not serving the expected deployment on port $Port." }
    if (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue) { throw "Port $Port is occupied by an unexpected or unhealthy server. No process was stopped." }
    $node = (Get-Command node.exe -ErrorAction Stop).Source
    $vite = Join-Path $app 'node_modules\vite\bin\vite.js'
    if (-not (Test-Path -LiteralPath $vite)) { throw 'Dependencies missing. Run npm ci in solana-devnet first.' }
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $web = Start-Process -FilePath $node -ArgumentList @(('"' + $vite + '"'), '--host', '127.0.0.1', '--port', $Port, '--strictPort') `
        -WorkingDirectory $app -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput (Join-Path $runtime "web-$stamp.log") `
        -RedirectStandardError (Join-Path $runtime "web-$stamp-error.log")
    $deadline = [DateTime]::UtcNow.AddSeconds(20)
    do {
        try {
            $served = Invoke-RestMethod -Uri ($url + 'deployment.json') -TimeoutSec 2
            $running = $served.pool -eq $deployment.pool -and $served.program -eq $deployment.program -and $served.rpc -eq $deployment.rpc
            if ($running) { break }
        } catch { }
        $web.Refresh()
        if ($web.HasExited) { throw "UI exited. Inspect .local/web-$stamp-error.log." }
        Start-Sleep -Milliseconds 300
    } while ([DateTime]::UtcNow -lt $deadline)
}
if (-not $running) { throw 'UI did not become healthy. Inspect .local logs; no automatic restart attempted.' }
Write-Host "Go Pools ready: $url"
if (-not $NoBrowser -and -not $CheckOnly) { Start-Process $url }
