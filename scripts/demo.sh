#!/usr/bin/env bash
# Demonstração do wallet-service: roda o fluxo principal e explica cada passo.
#
# Pré-requisito: o ambiente no ar (docker compose up --build -d).
# Uso:          ./scripts/demo.sh
#
# Não precisa editar nada: cada execução cria um jogador novo.
set -euo pipefail

API="${API:-http://localhost:8081}"
KEYCLOAK="${KEYCLOAK:-http://localhost:8080}"

# --- utilitários --------------------------------------------------------------

passo() { printf '\n\033[1;34m==> %s\033[0m\n' "$1"; }
ok()    { printf '\033[32m    OK: %s\033[0m\n' "$1"; }
falha() { printf '\033[31m    ERRO: %s\033[0m\n' "$1"; exit 1; }
mostra(){ printf '    resposta: %s\n' "$1"; }

# Gera um UUID sem depender de uuidgen (funciona em Linux, Mac e Git Bash).
novo_uuid() {
  od -An -tx1 -N16 /dev/urandom | tr -d ' \n' |
    sed -E 's/^(.{8})(.{4}).(.{3}).(.{3})(.{12})$/\1-\2-4\3-a\4-\5/'
}

# Lê um campo de texto de um JSON simples (sem precisar do jq).
campo() { sed -nE "s/.*\"$1\":\"([^\"]*)\".*/\1/p" <<<"$2"; }

token() {
  local resp
  resp=$(curl -s -d grant_type=client_credentials -d "client_id=$1" -d "client_secret=$2" \
    "$KEYCLOAK/realms/wallet/protocol/openid-connect/token")
  campo access_token "$resp"
}

# --- 1. ambiente ----------------------------------------------------------------

passo "1. Conferindo se o serviço está no ar"
saude=$(curl -s "$API/health/ready" || true)
[[ "$saude" == *'"status":"UP"'* ]] || falha "o serviço não respondeu. Rode 'docker compose up --build -d' e espere ficar pronto."
mostra "$saude"
ok "banco de dados e fila respondendo"

# --- 2. login -------------------------------------------------------------------

passo "2. Pegando os tokens de acesso no Keycloak"
OPERADOR=$(token wallet-backoffice wallet-backoffice-local-secret)
PROVEDOR=$(token provider-a provider-a-local-secret)
[[ -n "$OPERADOR" && -n "$PROVEDOR" ]] || falha "não consegui pegar os tokens. O Keycloak já terminou de subir?"
ok "token do serviço interno (abre carteiras) e do provedor 'provider-a' (faz apostas)"

# --- 3. carteira ----------------------------------------------------------------

passo "3. Abrindo uma carteira com R\$ 100,00 para um jogador novo"
JOGADOR=$(novo_uuid)
resp=$(curl -s -X POST "$API/wallets" -H "Authorization: Bearer $OPERADOR" \
  -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$JOGADOR\",\"initialBalance\":{\"amount\":\"100.00\",\"currency\":\"BRL\"}}")
mostra "$resp"
CARTEIRA=$(campo id "$resp")
[[ -n "$CARTEIRA" ]] || falha "a carteira não foi criada"
ok "carteira $CARTEIRA criada com saldo 100.00"

# Monta o corpo de uma operação do provider-a nesta carteira.
operacao() { # tipo valor idExterno [referência]
  local ref=""
  [[ -n "${4:-}" ]] && ref=",\"referenceExternalTransactionId\":\"$4\""
  printf '{"providerId":"provider-a","externalTransactionId":"%s","playerId":"%s","walletId":"%s","roundId":"rodada-1","gameId":"fortune-chimp","kind":"%s","money":{"amount":"%s","currency":"BRL"}%s}' \
    "$3" "$JOGADOR" "$CARTEIRA" "$1" "$2" "$ref"
}
enviar() { # corpo idExterno
  curl -s -X POST "$API/wagering/transactions" -H "Authorization: Bearer $PROVEDOR" \
    -H "Idempotency-Key: provider-a:$2" -H 'Content-Type: application/json' -d "$1"
}

# --- 4. aposta --------------------------------------------------------------------

passo "4. Fazendo uma aposta (BET) de R\$ 25,00"
APOSTA="aposta-$(novo_uuid | cut -c1-8)"
resp=$(enviar "$(operacao BET 25.00 "$APOSTA")" "$APOSTA")
mostra "$resp"
[[ "$resp" == *'"amount":"75.00"'* ]] || falha "o saldo deveria ser 75.00"
ok "aposta processada, saldo agora é 75.00"

passo "5. Enviando A MESMA aposta de novo (simulando um reenvio por falha de rede)"
resp=$(enviar "$(operacao BET 25.00 "$APOSTA")" "$APOSTA")
mostra "$resp"
[[ "$resp" == *'"idempotentReplay":true'* && "$resp" == *'"amount":"75.00"'* ]] || falha "deveria ser um replay"
ok "não cobrou duas vezes: devolveu o resultado original (idempotentReplay: true)"

passo "6. Tentando apostar R\$ 80,00 com saldo de R\$ 75,00"
OUTRA="aposta-$(novo_uuid | cut -c1-8)"
resp=$(enviar "$(operacao BET 80.00 "$OUTRA")" "$OUTRA")
mostra "$resp"
[[ "$resp" == *'INSUFFICIENT_FUNDS'* ]] || falha "deveria recusar por saldo insuficiente"
ok "recusada (INSUFFICIENT_FUNDS); o saldo nunca fica negativo"

passo "7. Devolvendo a primeira aposta (REFUND)"
DEVOL="devolucao-$(novo_uuid | cut -c1-8)"
resp=$(enviar "$(operacao REFUND 25.00 "$DEVOL" "$APOSTA")" "$DEVOL")
mostra "$resp"
[[ "$resp" == *'"amount":"100.00"'* ]] || falha "o saldo deveria voltar para 100.00"
ok "aposta devolvida, saldo de volta a 100.00"

# --- 8. extrato e conferência ------------------------------------------------------

passo "8. Extrato da carteira (ledger)"
resp=$(curl -s "$API/wallets/$CARTEIRA/ledger" -H "Authorization: Bearer $OPERADOR")
mostra "$resp"
ok "cada movimentação de dinheiro tem uma linha no extrato"

passo "9. Conferindo se o saldo bate com o extrato (reconciliação)"
resp=$(curl -s -X POST "$API/wallets/$CARTEIRA/reconciliation" -H "Authorization: Bearer $OPERADOR")
mostra "$resp"
[[ "$resp" == *'"consistent":true'* ]] || falha "saldo e extrato deveriam bater"
ok "saldo guardado = soma do extrato"

# --- 10. segurança -----------------------------------------------------------------

passo "10. Tentando consultar a carteira sem token"
codigo=$(curl -s -o /dev/null -w '%{http_code}' "$API/wallets/$CARTEIRA")
[[ "$codigo" == "401" ]] || falha "deveria responder 401"
ok "acesso negado (401) sem login"

passo "11. Provedor tentando abrir carteira (só o serviço interno pode)"
codigo=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/wallets" -H "Authorization: Bearer $PROVEDOR" \
  -H 'Content-Type: application/json' -d '{}')
[[ "$codigo" == "403" ]] || falha "deveria responder 403"
ok "acesso negado (403) por falta de permissão"

# --- 12. fila -----------------------------------------------------------------------

passo "12. Enviando uma aposta pela fila SQS (em vez de HTTP)"
PELA_FILA="fila-$(novo_uuid | cut -c1-8)"
MSG="msg-$(novo_uuid | cut -c1-8)"
CORPO="{\"messageId\":\"$MSG\",\"type\":\"WagerTransactionRequested\",\"occurredAt\":\"2026-09-08T12:00:00.000Z\",\"data\":$(operacao BET 10.00 "$PELA_FILA" | sed "s/}\$/,\"idempotencyKey\":\"provider-a:$PELA_FILA\"}/")}"
# O comando vai pela entrada padrão para não sofrer com aspas no terminal.
printf "awslocal sqs send-message --queue-url http://localhost:4566/000000000000/wager-transactions.fifo --message-group-id %s --message-deduplication-id %s --message-body '%s' >/dev/null\n" \
  "$CARTEIRA" "$MSG" "$CORPO" | docker compose exec -T localstack sh
for _ in $(seq 1 20); do
  resp=$(curl -s "$API/providers/provider-a/wagering/transactions/$PELA_FILA" -H "Authorization: Bearer $PROVEDOR")
  [[ "$resp" == *'"status":"PROCESSED"'* ]] && break
  sleep 1
done
mostra "$resp"
[[ "$resp" == *'"status":"PROCESSED"'* ]] || falha "a mensagem não foi processada"
ok "mensagem da fila processada com as mesmas regras do HTTP"

printf '\n\033[1;32mTudo certo! Demonstração concluída.\033[0m\n'
