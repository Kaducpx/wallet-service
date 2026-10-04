# wallet-service

Serviço de **carteira digital para apostas**. Ele guarda o saldo de cada
jogador e registra apostas, ganhos e devoluções enviadas pelos provedores de
jogos.

O foco é **nunca errar com dinheiro**, mesmo quando algo dá errado:

- o mesmo pedido enviado duas vezes **não cobra duas vezes**;
- o saldo **nunca fica negativo**, nem com várias apostas chegando ao mesmo tempo;
- toda movimentação fica registrada num **extrato que não pode ser alterado**;
- funciona com **vários servidores ao mesmo tempo** e se recupera de quedas.

> As decisões técnicas estão explicadas em [ARCHITECTURE.md](ARCHITECTURE.md).
> O guia de testes está em [TESTES.md](TESTES.md).

---

## Início rápido

São 3 passos. Na primeira vez, leva uns 5 minutos (a maior parte é download).

### 1. Instale o que precisa

| Programa | Para quê | Onde baixar |
|---|---|---|
| **Docker Desktop** | rodar o serviço e tudo que ele usa | https://www.docker.com/products/docker-desktop |
| **Go 1.27+** | só para rodar os testes | https://go.dev/dl |

Abra o Docker Desktop e espere aparecer **"Engine running"** no canto inferior
esquerdo.

### 2. Suba o serviço

Abra um terminal **na pasta do projeto** e rode:

```bash
docker compose up --build -d
```

Isso baixa e liga tudo sozinho: o banco de dados, o servidor de login
(Keycloak), a fila de mensagens (LocalStack) e o próprio serviço. O `-d` deixa
tudo rodando em segundo plano e devolve o terminal.

Para saber se já terminou de subir:

```bash
docker compose ps
```

Está pronto quando `postgres`, `keycloak`, `localstack` e `app` aparecem como
**`(healthy)`**. O `migrate` aparece como **`Exited (0)`**: é normal, ele só
prepara o banco e encerra.

> O Keycloak é o mais lento: pode levar 1 ou 2 minutos para ficar `healthy`.

### 3. Veja funcionando

Rode o script de demonstração. Ele faz o fluxo completo sozinho e explica cada
passo na tela, sem precisar editar nada:

**Windows (PowerShell):**

```bash
powershell -ExecutionPolicy Bypass -File scripts\demo.ps1
```

**Linux, Mac ou Git Bash:**

```bash
./scripts/demo.sh
```

O que ele faz:

1. confere se o serviço está no ar;
2. faz login no Keycloak;
3. abre uma carteira com R$ 100,00;
4. faz uma aposta de R$ 25,00 (saldo vai para 75,00);
5. manda **a mesma aposta de novo** e mostra que não foi cobrada duas vezes;
6. tenta apostar R$ 80,00 e é recusado por falta de saldo;
7. devolve a primeira aposta (saldo volta para 100,00);
8. mostra o extrato;
9. confere se o saldo bate com o extrato;
10. tenta acessar sem login (recusado);
11. tenta fazer algo sem permissão (recusado);
12. manda uma aposta pela fila de mensagens em vez do HTTP.

No final deve aparecer:

```
Tudo certo! Demonstração concluída.
```

### Para desligar

```bash
docker compose --profile multi down
```

O `--profile multi` garante que os servidores extras do modo com 3 servidores
também sejam desligados (se você não usou esse modo, não faz diferença).

---

## Rodando os testes

> Guia completo, com preparação das dependências, build tags, múltiplas
> instâncias e simulações de falha: **[TESTES.md](TESTES.md)**.

Os testes precisam do **Go** instalado. Rode os comandos **na pasta do projeto**.

### Testes rápidos (não precisam do Docker)

```bash
go test ./...
```

```bash
go test -race ./...
```

```bash
go vet ./...
```

O que esperar:

- `go test` e `go test -race`: linhas com `ok` para os pacotes com testes.
  As linhas `[no test files]` **não são erro**: esses pacotes são testados
  na suíte de integração, logo abaixo.
- `go vet`: **não mostra nada**. Silêncio quer dizer que está tudo certo.

> O `-race` precisa de um compilador C. No Linux e no Mac normalmente já vem
> instalado; no Windows é preciso o MinGW (gcc).

### Testes de integração (usam banco, login e fila de verdade)

Primeiro, ligue as dependências (se o passo 2 do início rápido já foi feito,
elas já estão ligadas):

```bash
docker compose up -d postgres keycloak localstack
```

Depois rode a suíte (leva uns 2 minutos):

```bash
go test -tags integration -count=1 -v ./test/integration/...
```

Cada teste mostra `--- PASS` e no final aparece:

```
PASS
ok      wallet-service/test/integration
```

> No meio da saída pode aparecer uma linha com `"level":"ERROR"` e
> `SERVICE_UNAVAILABLE`. **É esperado**: um dos testes desliga o banco de
> propósito para provar que o serviço responde corretamente nessa situação.

A mesma suíte com o detector de problemas de concorrência:

```bash
go test -race -tags integration -count=1 ./test/integration/...
```

Os testes usam um banco separado (`wallet_it`) e filas próprias. Eles **não
mexem** nos dados do serviço que você subiu no início rápido.

### O que cada teste prova

| O que o desafio pede | Teste |
|---|---|
| Duas apostas de 80 ao mesmo tempo com saldo de 100 | `TestTwoConcurrentBetsOf80` |
| A mesma aposta enviada 50 vezes ao mesmo tempo | `TestSameBet50TimesInParallel` |
| Carteiras diferentes andando em paralelo | `TestDistinctWalletsProgressInParallel` |
| 3 servidores independentes ao mesmo tempo | `TestThreeInstances` |
| Servidor cai depois de gravar e antes de tirar a mensagem da fila | `TestConsumerCrashAfterCommitIsRedelivered` |
| Servidor cai antes de publicar o evento | `TestCrashBetweenCommitAndPublish` |
| Servidor cai depois de publicar e antes de confirmar | `TestCrashBetweenPublishAndConfirm` |
| Dois publicadores disputando os mesmos eventos | `TestTwoPublishersCompeteForOutbox` |
| Devolução que chega antes da aposta | `TestReversalBeforeReference` |
| Reiniciar tudo sem perder nada | `TestRestartPreservesState` |
| A mesma operação chegando por HTTP e pela fila | `TestHTTPAndSQSSameOperation` |
| Login obrigatório e um provedor não vê o do outro | `TestAuthenticationIsRequired`, `TestAuthorizationAndProviderIsolation` |
| Banco de dados fora do ar | `TestPostgresOutage` |
| Fila de mensagens fora do ar | `TestSQSOutage` |
| Regras garantidas pelo próprio banco | `TestDatabaseInvariants`, `TestMigrationsUpDownUp` |
| Mensagens com erro indo para a fila de erros (DLQ) | `TestSQSInvalidAndConflictingMessagesGoToDLQ`, `TestSQSExhaustedRetriesGoToDLQ` |
| Ligar e desligar o serviço corretamente | `TestFxLifecycle` |

Para rodar só um deles, troque o nome depois do `-run`:

```bash
go test -tags integration -count=1 -v -run TestTwoConcurrentBetsOf80 ./test/integration/...
```

> Os testes de "fora do ar" pausam os containers com o comando `docker pause`.
> Por isso precisam do Docker ligado.

---

## Problemas comuns

**"port is already allocated" ao subir**
Algum programa já usa uma das portas (8080, 8081, 4566 ou 55432). Feche o
programa ou mude a porta no arquivo `.env` (copie o `.env.example` para `.env`
e edite).

**O `keycloak` demora ou o script diz que não conseguiu o token**
O Keycloak leva 1 ou 2 minutos para ficar pronto. Espere aparecer `(healthy)`
em `docker compose ps` e tente de novo.

**No Windows: "a execução de scripts foi desabilitada"**
Use exatamente o comando do passo 3, com `-ExecutionPolicy Bypass`. Ele libera
só aquele script, sem mudar nada no seu Windows.

**No Linux/Mac: "Permission denied" ao rodar o script**
Rode `chmod +x scripts/demo.sh` uma vez e tente de novo.

**`go test -race` falha dizendo que precisa de cgo ou gcc**
Falta o compilador C (veja a observação na seção de testes). Os outros
comandos funcionam sem ele.

**Quero começar do zero (apagar os dados)**

```bash
docker compose --profile multi down -v
```

O `-v` apaga também os dados do banco.

---

## Testando na mão (opcional)

Se quiser fazer as chamadas uma a uma, em vez de usar o script.

### Quem pode fazer o quê

O login é feito no Keycloak. Já existem estes usuários de sistema (só para uso
local):

| Usuário (client_id) | Senha (client_secret) | Pode |
|---|---|---|
| `wallet-backoffice` | `wallet-backoffice-local-secret` | abrir carteiras, ver extrato, conferir saldo |
| `provider-a` | `provider-a-local-secret` | enviar apostas em nome do `provider-a` |
| `provider-b` | `provider-b-local-secret` | enviar apostas em nome do `provider-b` |
| `provider-a-short-lived` | `provider-a-short-local-secret` | igual ao `provider-a`, mas o login vale só 3 segundos (para testar expiração) |
| `no-role-client` | `no-role-local-secret` | nada (para testar acesso negado) |

O painel do Keycloak fica em http://localhost:8080 (usuário `admin`, senha
`admin`).

### Passo a passo (Linux, Mac ou Git Bash)

Os comandos abaixo guardam o token e o id da carteira em variáveis, então dá
para copiar e colar em sequência sem editar nada.

**1. Fazer login:**

```bash
OPERADOR=$(curl -s -d grant_type=client_credentials -d client_id=wallet-backoffice -d client_secret=wallet-backoffice-local-secret http://localhost:8080/realms/wallet/protocol/openid-connect/token | sed -E 's/.*"access_token":"([^"]+)".*/\1/')
```

```bash
PROVEDOR=$(curl -s -d grant_type=client_credentials -d client_id=provider-a -d client_secret=provider-a-local-secret http://localhost:8080/realms/wallet/protocol/openid-connect/token | sed -E 's/.*"access_token":"([^"]+)".*/\1/')
```

**2. Abrir uma carteira com R$ 1.000,00** (cada execução usa um jogador novo):

```bash
JOGADOR=$(od -An -tx1 -N16 /dev/urandom | tr -d ' \n' | sed -E 's/^(.{8})(.{4}).(.{3}).(.{3})(.{12})$/\1-\2-4\3-a\4-\5/')
```

```bash
CARTEIRA=$(curl -s -X POST http://localhost:8081/wallets -H "Authorization: Bearer $OPERADOR" -H 'Content-Type: application/json' -d "{\"playerId\":\"$JOGADOR\",\"initialBalance\":{\"amount\":\"1000.00\",\"currency\":\"BRL\"}}" | sed -E 's/.*"id":"([^"]+)".*/\1/'); echo "carteira: $CARTEIRA"
```

**3. Fazer uma aposta de R$ 25,00:**

```bash
APOSTA="aposta-$RANDOM"; curl -s -X POST http://localhost:8081/wagering/transactions -H "Authorization: Bearer $PROVEDOR" -H "Idempotency-Key: provider-a:$APOSTA" -H 'Content-Type: application/json' -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$APOSTA\",\"playerId\":\"$JOGADOR\",\"walletId\":\"$CARTEIRA\",\"roundId\":\"rodada-1\",\"gameId\":\"fortune-chimp\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}"
```

Resposta esperada:

```json
{"transactionId":"...","status":"PROCESSED","balance":{"amount":"975.00","currency":"BRL"},"idempotentReplay":false}
```

Rodando **o mesmo comando de novo** (sem mudar a variável `APOSTA`), a resposta
é igual, mas com `"idempotentReplay":true` e o saldo continua `975.00`: o
pedido repetido não cobrou de novo.

**4. Ver o extrato e conferir o saldo:**

```bash
curl -s http://localhost:8081/wallets/$CARTEIRA/ledger -H "Authorization: Bearer $OPERADOR"
```

```bash
curl -s -X POST http://localhost:8081/wallets/$CARTEIRA/reconciliation -H "Authorization: Bearer $OPERADOR"
```

A conferência deve responder `"consistent":true`.

### Tipos de operação

| Tipo | O que faz | Valor |
|---|---|---|
| `BET` | aposta: tira dinheiro da carteira | maior que zero |
| `WIN` | ganho: põe dinheiro na carteira | maior que zero |
| `LOSS` | perda: só registra, não mexe no saldo (o dinheiro já saiu na aposta) | sempre `0.00` |
| `REFUND` | devolve uma aposta inteira | igual ao da aposta |
| `ROLLBACK` | desfaz uma aposta, ganho ou devolução | igual ao original |

`REFUND` e `ROLLBACK` precisam do campo `referenceExternalTransactionId`
apontando para a operação original.

### Respostas da API

| Código | Quando |
|---|---|
| `201` | operação feita |
| `200` | pedido repetido: devolve o resultado da primeira vez |
| `202` | devolução esperando a aposta original chegar |
| `400` | dados inválidos (nada foi gravado) |
| `401` | sem login, ou login inválido ou expirado |
| `403` | logado, mas sem permissão |
| `404` | carteira ou transação não encontrada |
| `409` | conflito (ex.: mesma chave com dados diferentes) |
| `422` | recusada por regra de negócio (ex.: saldo insuficiente) |
| `503` | algo que o serviço usa está fora do ar; tente de novo |

Detalhes completos em [ARCHITECTURE.md, seção "Contrato HTTP"](ARCHITECTURE.md#11-contrato-http).

---

## Detalhes técnicos

### Tecnologias

| Para quê | O que foi usado |
|---|---|
| Linguagem | Go 1.27 |
| Montagem da aplicação | Uber Fx |
| API | `net/http` (biblioteca padrão do Go) |
| Banco de dados | PostgreSQL 17, com `pgx` e SQL escrito à mão |
| Versões do banco (migrations) | `golang-migrate` |
| Fila de mensagens | AWS SQS, simulado localmente pelo LocalStack 4.9 |
| Login | Keycloak 26 (OAuth 2.0 / OpenID Connect) |
| Logs e métricas | `log/slog` (JSON) e Prometheus |

### Endereços

| Serviço | Endereço |
|---|---|
| API | http://localhost:8081 |
| API, segundo e terceiro servidor (modo com 3 servidores) | http://localhost:8082 e http://localhost:8083 |
| Keycloak | http://localhost:8080 |
| LocalStack (fila) | http://localhost:4566 |
| PostgreSQL | `localhost:55432` |
| Saúde do serviço | http://localhost:8081/health/ready |
| Métricas | http://localhost:8081/metrics |

### Rodando com 3 servidores

```bash
docker compose --profile multi up --build -d
```

Sobe três cópias do serviço (portas 8081, 8082 e 8083) usando o mesmo banco e
a mesma fila. Para desligar:

```bash
docker compose --profile multi down
```

Para simular a queda de um servidor: `docker compose kill app-2`. Para um
desligamento normal: `docker compose stop app-2`.

### Filas

São criadas automaticamente quando o LocalStack liga:

| Fila | Para quê |
|---|---|
| `wager-transactions.fifo` | operações chegando pela fila |
| `wager-transactions-dlq.fifo` | mensagens com erro, ou que falharam 5 vezes (DLQ) |
| `wallet-events.fifo` | avisos que o serviço publica (aposta processada, saldo mudou...) |

Para recriar as filas na mão:

```bash
docker compose exec localstack bash /etc/localstack/init/ready.d/init-sqs.sh
```

### Banco de dados e migrations

As tabelas são criadas pelas migrations (pasta [`migrations/`](migrations)),
aplicadas sozinhas quando o serviço sobe. Para aplicar ou desfazer na mão:

```bash
docker compose run --rm migrate migrate up
```

```bash
docker compose run --rm migrate migrate down 1
```

```bash
docker compose run --rm migrate migrate version
```

`migrate down` sem número desfaz todas. O serviço conecta no banco com um
usuário restrito (`wallet_app`), que não consegue apagar nem alterar o extrato.

### Configuração (variáveis de ambiente)

Os valores de exemplo estão em [.env.example](.env.example). O Docker Compose
já usa valores prontos; só precisa mexer se quiser mudar algo.

| Variável | Padrão | Para quê |
|---|---|---|
| `DATABASE_URL` | obrigatória | conexão com o banco (usuário restrito) |
| `MIGRATIONS_DATABASE_URL` | — | conexão usada pelas migrations (dono das tabelas) |
| `HTTP_ADDR` | `:8081` | porta da API |
| `HTTP_REQUEST_TIMEOUT` | `10s` | tempo máximo de cada requisição |
| `OIDC_ISSUER` | obrigatória | endereço do Keycloak que emite os logins |
| `OIDC_JWKS_URL` | obrigatória | onde buscar as chaves do Keycloak |
| `OIDC_AUDIENCE` | `wallet-service` | para quem o login precisa ter sido emitido |
| `AWS_ENDPOINT_URL` | — | endereço do LocalStack (vazio na AWS de verdade) |
| `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | — | acesso à fila |
| `SQS_QUEUE_NAME`, `SQS_DLQ_NAME`, `SQS_EVENTS_QUEUE_NAME` | nomes da tabela de filas | nomes das filas |
| `SQS_CONSUMER_ENABLED` | `true` | liga a leitura da fila |
| `SQS_CONSUMER_WORKERS` | `4` | quantas leituras da fila em paralelo |
| `SQS_RETRY_BASE` / `SQS_RETRY_MAX` | `2s` / `60s` | espera entre tentativas de uma mensagem |
| `SQS_DLQ_MONITOR_INTERVAL` | `15s` | de quanto em quanto tempo mede a DLQ |
| `OUTBOX_ENABLED` | `true` | liga a publicação de eventos |
| `OUTBOX_LEASE` | `30s` | tempo de reserva de um lote de eventos |
| `OUTBOX_RETRY_BASE` / `OUTBOX_RETRY_MAX` | `1s` / `5m` | espera entre tentativas de publicação |
| `PENDING_TTL` | `10m` | quanto tempo uma devolução espera a aposta original |
| `PENDING_MAX_ATTEMPTS` | `20` | quantas vezes tenta antes de desistir |
| `PENDING_BACKOFF_BASE` / `PENDING_BACKOFF_MAX` | `1s` / `1m` | espera entre essas tentativas |
| `SHUTDOWN_TIMEOUT` | `25s` | tempo para desligar com calma |
| `DB_LOCK_TIMEOUT` | `5s` | tempo máximo esperando a vez numa carteira |
| `FAULT_POINTS` | vazio | simula falhas (**só para testes**, ver [TESTES.md](TESTES.md#4-simulações-de-falha)) |

Se alguma configuração estiver errada, o serviço não sobe e mostra o motivo.

### Logs, métricas e saúde

- **Logs** em JSON, com os ids que permitem seguir uma operação de ponta a
  ponta (`correlationId`, `messageId`, `transactionId`, `walletId`,
  `providerId`). Senhas, tokens e dados financeiros completos nunca vão para o
  log.
- **Métricas** em `/metrics`: resultados por status, pedidos repetidos,
  conflitos, novas tentativas, DLQ, atraso na publicação de eventos, tempo de
  resposta e divergências de saldo. A lista completa está no
  [ARCHITECTURE.md](ARCHITECTURE.md#16-observabilidade).
- **Saúde**: `/health/live` (o processo está de pé) e `/health/ready` (o banco
  e a fila estão respondendo).

### Organização do código

```
cmd/wallet-service/     ponto de entrada (sobe o serviço ou roda migrations)
internal/
  domain/               regras de negócio (sem banco, sem HTTP)
    money/              dinheiro sem usar números quebrados (float)
  app/                  casos de uso: o passo a passo de cada operação
  adapters/
    postgres/           acesso ao banco
    sqs/                leitura e escrita nas filas
    httpapi/            rotas da API
  auth/                 validação do login
  worker/               tarefas em segundo plano
  bootstrap/            monta e liga todas as peças
  platform/             configuração, logs e métricas
migrations/             criação das tabelas
deploy/                 configuração inicial do banco, do Keycloak e das filas
scripts/                scripts de demonstração
test/integration/       testes com banco, login e fila de verdade
```

Cada pasta tem um arquivo `doc.go` explicando o que tem nela.

**Por onde começar a ler o código:**

1. `internal/domain/money` – como o dinheiro é guardado sem erro de arredondamento.
2. `internal/domain/wallet.go` e `settlement.go` – as regras de cada operação.
3. `internal/app/wager_submit.go` – o caminho completo de uma aposta.
4. `migrations/000001_init.up.sql` – as regras que o próprio banco garante.
5. `internal/adapters/httpapi/routes.go` – todas as rotas e quem pode usar cada uma.
6. `internal/bootstrap/bootstrap.go` – como tudo é montado e desligado.
