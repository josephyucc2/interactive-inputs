[CmdletBinding()]
param(
    [ValidateSet("Install", "Uninstall", "Status")]
    [string]$Action = "Install",
    [string]$InstallDirectory = "$env:ProgramData\InteractiveInputs",
    [int]$Port = 9090,
    [int]$SessionTimeout = 300,
    [string]$Executable = "",
    [string]$TaskName = "Interactive Inputs Server",
    [string]$FirewallRuleName = "Interactive Inputs Server (LAN)",
    [switch]$RemoveFiles
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw "Run this script from an Administrator PowerShell terminal."
    }
}

function Get-DefaultExecutable {
    $architecture = $env:PROCESSOR_ARCHITECTURE
    $binaryName = if ($architecture -eq "ARM64") {
        "action-windows-arm64.exe"
    } else {
        "action-windows-amd64.exe"
    }
    return Join-Path (Split-Path $PSScriptRoot -Parent) "dist\$binaryName"
}

function Test-ServerHealth {
    try {
        $response = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/health" -TimeoutSec 3
        return $response.status -eq "ok"
    } catch {
        return $false
    }
}

if ($env:OS -ne "Windows_NT") {
    throw "This installer only supports Windows."
}
if ($Port -lt 1 -or $Port -gt 65535) {
    throw "Port must be between 1 and 65535."
}

Assert-Administrator

if ($Action -eq "Status") {
    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($null -eq $task) {
        Write-Host "Scheduled task is not installed: $TaskName"
        exit 1
    }
    $taskInfo = Get-ScheduledTaskInfo -TaskName $TaskName
    Write-Host "Task state: $($task.State)"
    Write-Host "Last result: $($taskInfo.LastTaskResult)"
    Write-Host "Health: $(if (Test-ServerHealth) { 'ok' } else { 'unreachable' })"
    exit 0
}

if ($Action -eq "Uninstall") {
    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($null -ne $task) {
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
    }
    Get-NetFirewallRule -DisplayName $FirewallRuleName -ErrorAction SilentlyContinue |
        Remove-NetFirewallRule -ErrorAction SilentlyContinue
    if ($RemoveFiles -and (Test-Path -LiteralPath $InstallDirectory)) {
        Remove-Item -LiteralPath $InstallDirectory -Recurse -Force
    }
    Write-Host "Interactive Inputs server uninstalled."
    exit 0
}

if (-not $Executable) {
    $Executable = Get-DefaultExecutable
}
if (-not (Test-Path -LiteralPath $Executable -PathType Leaf)) {
    throw "Windows binary not found: $Executable. Run the Windows build first."
}

New-Item -ItemType Directory -Path $InstallDirectory -Force | Out-Null
$targetExecutable = Join-Path $InstallDirectory "interactive-inputs.exe"

$existingTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
if ($null -ne $existingTask) {
    Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
}
Copy-Item -LiteralPath $Executable -Destination $targetExecutable -Force

$taskAction = New-ScheduledTaskAction `
    -Execute $targetExecutable `
    -Argument "--server --port $Port --session-timeout $SessionTimeout" `
    -WorkingDirectory $InstallDirectory
$trigger = New-ScheduledTaskTrigger -AtStartup
$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -RestartCount 999 `
    -RestartInterval (New-TimeSpan -Minutes 1)

Register-ScheduledTask `
    -TaskName $TaskName `
    -Action $taskAction `
    -Trigger $trigger `
    -Settings $settings `
    -User "SYSTEM" `
    -RunLevel Highest `
    -Force | Out-Null

Get-NetFirewallRule -DisplayName $FirewallRuleName -ErrorAction SilentlyContinue |
    Remove-NetFirewallRule -ErrorAction SilentlyContinue
New-NetFirewallRule `
    -DisplayName $FirewallRuleName `
    -Direction Inbound `
    -Action Allow `
    -Protocol TCP `
    -LocalPort $Port `
    -RemoteAddress LocalSubnet | Out-Null

Start-ScheduledTask -TaskName $TaskName
for ($attempt = 1; $attempt -le 30; $attempt++) {
    if (Test-ServerHealth) {
        Write-Host "Interactive Inputs server is ready on port $Port."
        Write-Host "Health URL: http://127.0.0.1:$Port/health"
        exit 0
    }
    Start-Sleep -Seconds 1
}

throw "Scheduled task started, but the health endpoint did not become ready within 30 seconds."

