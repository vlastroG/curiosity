# Открывает порт doc-index (5180) в брандмауэре Windows только для частной сети и
# только для адресов своей подсети. Запускать от администратора:
#   powershell -ExecutionPolicy Bypass -File src\doc-index\lan-firewall.ps1
# Убрать правило: Remove-NetFirewallRule -DisplayName "Twain Expert (doc-index)"
#Requires -RunAsAdministrator

$name = "Twain Expert (doc-index)"
$port = 5180

Get-NetFirewallRule -DisplayName $name -ErrorAction SilentlyContinue | Remove-NetFirewallRule
New-NetFirewallRule -DisplayName $name -Direction Inbound -Action Allow `
    -Protocol TCP -LocalPort $port -Profile Private -RemoteAddress LocalSubnet | Out-Null
Write-Host "Правило '$name': TCP $port, частная сеть, только локальная подсеть."

# правило действует только в частной сети; публичную (кафе, гостиница) не трогаем
foreach ($p in Get-NetConnectionProfile) {
    if ($p.NetworkCategory -ne "Private") {
        Write-Warning "Сеть '$($p.Name)' ($($p.InterfaceAlias)) -- $($p.NetworkCategory): из неё doc-index недоступен. Если это домашняя сеть, сделайте её частной: Параметры -> Сеть и Интернет -> свойства сети."
    }
}

Write-Host "Адреса этой машины в сети:"
Get-NetIPAddress -AddressFamily IPv4 -PrefixOrigin Dhcp, Manual -ErrorAction SilentlyContinue |
    Where-Object { $_.IPAddress -notlike "169.254.*" -and $_.InterfaceAlias -notlike "vEthernet*" } |
    ForEach-Object { Write-Host "  http://$($_.IPAddress):$port  ($($_.InterfaceAlias))" }
Write-Host "  http://$($env:COMPUTERNAME.ToLower()):$port  (по имени, если сеть его разрешает)"
