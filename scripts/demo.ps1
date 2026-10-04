# Demonstração do wallet-service (versão Windows / PowerShell).
# Roda o fluxo principal e explica cada passo.
#
# Pré-requisito: o ambiente no ar (docker compose up --build -d).
# Uso (na pasta do projeto):
#   powershell -ExecutionPolicy Bypass -File scripts\demo.ps1
#
# Não precisa editar nada: cada execução cria um jogador novo.
$ErrorActionPreference = 'Stop'

$Api = if ($env:API) { $env:API } else { 'http://localhost:8081' }
$Keycloak = if ($env:KEYCLOAK) { $env:KEYCLOAK } else { 'http://localhost:8080' }

# --- utilitários --------------------------------------------------------------

function Passo($texto) { Write-Host ""; Write-Host "==> $texto" -ForegroundColor Cyan }
function Ok($texto) { Write-Host "    OK: $texto" -ForegroundColor Green }
function Falha($texto) { Write-Host "    ERRO: $texto" -ForegroundColor Red; exit 1 }
function Mostra($texto) { Write-Host "    resposta: $($texto.Trim())" }
function NovoId { [guid]::NewGuid().ToString() }
function Curto { (NovoId).Substring(0, 8) }

# Faz uma chamada HTTP e devolve o status e o corpo, mesmo quando a API
# responde com erro (4xx/5xx). Funciona no PowerShell 5.1 e no 7.
function Chamar($Metodo, $Url, $Token, $Corpo, $Extras) {
    $cabecalhos = @{}
    if ($Token) { $cabecalhos['Authorization'] = "Bearer $Token" }
    if ($Extras) { foreach ($k in $Extras.Keys) { $cabecalhos[$k] = $Extras[$k] } }
    $params = @{ Method = $Metodo; Uri = $Url; Headers = $cabecalhos; UseBasicParsing = $true }
    if ($Corpo) { $params['Body'] = $Corpo; $params['ContentType'] = 'application/json' }
    try {
        $r = Invoke-WebRequest @params
        return @{ Status = [int]$r.StatusCode; Corpo = $r.Content }
    } catch {
        $resp = $_.Exception.Response
        if (-not $resp) { throw }
        $texto = $_.ErrorDetails.Message
        if (-not $texto) {
            try {
                $leitor = New-Object System.IO.StreamReader($resp.GetResponseStream())
                $texto = $leitor.ReadToEnd()
            } catch { $texto = '' }
        }
        return @{ Status = [int]$resp.StatusCode; Corpo = $texto }
    }
}

function Token($cliente, $segredo) {
    $r = Invoke-RestMethod -Method Post -Uri "$Keycloak/realms/wallet/protocol/openid-connect/token" `
        -Body @{ grant_type = 'client_credentials'; client_id = $cliente; client_secret = $segredo }
    return $r.access_token
}

# --- 1. ambiente ----------------------------------------------------------------

Passo "1. Conferindo se o serviço está no ar"
try { $saude = Chamar GET "$Api/health/ready" } catch { $saude = @{ Status = 0; Corpo = '' } }
if ($saude.Corpo -notmatch '"status":"UP"') {
    Falha "o serviço não respondeu. Rode 'docker compose up --build -d' e espere ficar pronto."
}
Mostra $saude.Corpo
Ok "banco de dados e fila respondendo"

# --- 2. login -------------------------------------------------------------------

Passo "2. Pegando os tokens de acesso no Keycloak"
try {
    $Operador = Token 'wallet-backoffice' 'wallet-backoffice-local-secret'
    $Provedor = Token 'provider-a' 'provider-a-local-secret'
} catch { Falha "não consegui pegar os tokens. O Keycloak já terminou de subir?" }
Ok "token do serviço interno (abre carteiras) e do provedor 'provider-a' (faz apostas)"

# --- 3. carteira ----------------------------------------------------------------

Passo "3. Abrindo uma carteira com R`$ 100,00 para um jogador novo"
$Jogador = NovoId
$corpo = "{`"playerId`":`"$Jogador`",`"initialBalance`":{`"amount`":`"100.00`",`"currency`":`"BRL`"}}"
$r = Chamar POST "$Api/wallets" $Operador $corpo
Mostra $r.Corpo
$Carteira = ($r.Corpo | ConvertFrom-Json).id
if (-not $Carteira) { Falha "a carteira não foi criada" }
Ok "carteira $Carteira criada com saldo 100.00"

# Monta o corpo de uma operação do provider-a nesta carteira.
function Operacao($tipo, $valor, $idExterno, $referencia) {
    $ref = ''
    if ($referencia) { $ref = ",`"referenceExternalTransactionId`":`"$referencia`"" }
    return "{`"providerId`":`"provider-a`",`"externalTransactionId`":`"$idExterno`",`"playerId`":`"$Jogador`",`"walletId`":`"$Carteira`",`"roundId`":`"rodada-1`",`"gameId`":`"fortune-chimp`",`"kind`":`"$tipo`",`"money`":{`"amount`":`"$valor`",`"currency`":`"BRL`"}$ref}"
}
function Enviar($corpo, $idExterno) {
    return Chamar POST "$Api/wagering/transactions" $Provedor $corpo @{ 'Idempotency-Key' = "provider-a:$idExterno" }
}

# --- 4. aposta ------------------------------------------------------------------

Passo "4. Fazendo uma aposta (BET) de R`$ 25,00"
$Aposta = "aposta-$(Curto)"
$r = Enviar (Operacao 'BET' '25.00' $Aposta) $Aposta
Mostra $r.Corpo
if ($r.Corpo -notmatch '"amount":"75.00"') { Falha "o saldo deveria ser 75.00" }
Ok "aposta processada, saldo agora é 75.00"

Passo "5. Enviando A MESMA aposta de novo (simulando um reenvio por falha de rede)"
$r = Enviar (Operacao 'BET' '25.00' $Aposta) $Aposta
Mostra $r.Corpo
if ($r.Corpo -notmatch '"idempotentReplay":true') { Falha "deveria ser um replay" }
Ok "não cobrou duas vezes: devolveu o resultado original (idempotentReplay: true)"

Passo "6. Tentando apostar R`$ 80,00 com saldo de R`$ 75,00"
$Outra = "aposta-$(Curto)"
$r = Enviar (Operacao 'BET' '80.00' $Outra) $Outra
Mostra $r.Corpo
if ($r.Corpo -notmatch 'INSUFFICIENT_FUNDS') { Falha "deveria recusar por saldo insuficiente" }
Ok "recusada (INSUFFICIENT_FUNDS); o saldo nunca fica negativo"

Passo "7. Devolvendo a primeira aposta (REFUND)"
$Devolucao = "devolucao-$(Curto)"
$r = Enviar (Operacao 'REFUND' '25.00' $Devolucao $Aposta) $Devolucao
Mostra $r.Corpo
if ($r.Corpo -notmatch '"amount":"100.00"') { Falha "o saldo deveria voltar para 100.00" }
Ok "aposta devolvida, saldo de volta a 100.00"

# --- 8. extrato e conferência ------------------------------------------------------

Passo "8. Extrato da carteira (ledger)"
$r = Chamar GET "$Api/wallets/$Carteira/ledger" $Operador
Mostra $r.Corpo
Ok "cada movimentação de dinheiro tem uma linha no extrato"

Passo "9. Conferindo se o saldo bate com o extrato (reconciliação)"
$r = Chamar POST "$Api/wallets/$Carteira/reconciliation" $Operador
Mostra $r.Corpo
if ($r.Corpo -notmatch '"consistent":true') { Falha "saldo e extrato deveriam bater" }
Ok "saldo guardado = soma do extrato"

# --- 10. segurança -----------------------------------------------------------------

Passo "10. Tentando consultar a carteira sem token"
$r = Chamar GET "$Api/wallets/$Carteira"
if ($r.Status -ne 401) { Falha "deveria responder 401, respondeu $($r.Status)" }
Ok "acesso negado (401) sem login"

Passo "11. Provedor tentando abrir carteira (só o serviço interno pode)"
$r = Chamar POST "$Api/wallets" $Provedor '{}'
if ($r.Status -ne 403) { Falha "deveria responder 403, respondeu $($r.Status)" }
Ok "acesso negado (403) por falta de permissão"

# --- 12. fila -----------------------------------------------------------------------

Passo "12. Enviando uma aposta pela fila SQS (em vez de HTTP)"
$PelaFila = "fila-$(Curto)"
$Msg = "msg-$(Curto)"
$dados = (Operacao 'BET' '10.00' $PelaFila) -replace '}$', ",`"idempotencyKey`":`"provider-a:$PelaFila`"}"
$mensagem = "{`"messageId`":`"$Msg`",`"type`":`"WagerTransactionRequested`",`"occurredAt`":`"2026-09-08T12:00:00.000Z`",`"data`":$dados}"
# A mensagem vai em base64 (só letras e números) para não ter problema com
# aspas nem com encoding no caminho até o container.
$b64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($mensagem))
$fila = "http://localhost:4566/000000000000/wager-transactions.fifo"
docker compose exec -T localstack sh -c "echo $b64 | base64 -d > /tmp/msg.json && awslocal sqs send-message --queue-url $fila --message-group-id $Carteira --message-deduplication-id $Msg --message-body file:///tmp/msg.json > /dev/null"
for ($i = 0; $i -lt 20; $i++) {
    $r = Chamar GET "$Api/providers/provider-a/wagering/transactions/$PelaFila" $Provedor
    if ($r.Corpo -match '"status":"PROCESSED"') { break }
    Start-Sleep -Seconds 1
}
Mostra $r.Corpo
if ($r.Corpo -notmatch '"status":"PROCESSED"') { Falha "a mensagem não foi processada" }
Ok "mensagem da fila processada com as mesmas regras do HTTP"

Write-Host ""
Write-Host "Tudo certo! Demonstração concluída." -ForegroundColor Green
