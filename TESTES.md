# Guia de testes

Como preparar o ambiente e rodar cada tipo de teste: **unitários**,
**integração**, **múltiplas instâncias** e **simulações de falha**.

Todos os comandos são rodados **na pasta do projeto**.

---

## 1. Preparar as dependências

| O que | Para quê | Obrigatório? |
|---|---|---|
| **Go 1.27+** | rodar os testes | sim |
| **Docker Desktop** | banco, login e fila usados nos testes de integração | só para integração |
| **Compilador C** (gcc) | testes com `-race` | só para `-race` |

> No Linux e no Mac o gcc normalmente já vem instalado. No Windows, instale o
> MinGW (gcc) e deixe-o no `PATH`.

Os testes de integração usam containers de verdade: PostgreSQL, Keycloak e
LocalStack (que simula o SQS da AWS). Para ligá-los:

```bash
docker compose up -d postgres keycloak localstack
```

Espere os três aparecerem como `(healthy)`:

```bash
docker compose ps
```

> Se você já subiu tudo com `docker compose up --build -d`, eles já estão
> ligados. Não precisa fazer de novo.

Os testes de integração **não mexem nos seus dados**. Eles criam sozinhos:

- um banco separado, `wallet_it`, recriado do zero a cada execução;
- filas SQS próprias para cada teste, apagadas no final;
- uma cópia compilada do serviço, usada nos testes com vários processos.

### Infraestrutura em outro lugar (opcional)

Por padrão, os testes procuram tudo em `localhost` com as portas do
`docker-compose.yml`. Para apontar para outro lugar, use estas variáveis:

| Variável | Padrão |
|---|---|
| `IT_PG_HOST` | `localhost:55432` |
| `IT_PG_OWNER` | `wallet_owner:wallet_owner_local` |
| `IT_PG_APP` | `wallet_app:wallet_app_local` |
| `IT_DB_NAME` | `wallet_it` |
| `IT_KEYCLOAK_URL` | `http://localhost:8080` |
| `IT_OIDC_ISSUER` | `http://localhost:8080/realms/wallet` |
| `IT_AWS_ENDPOINT` | `http://localhost:4566` |
| `IT_PG_CONTAINER` | `wallet-service-postgres-1` (usado nos testes de queda) |
| `IT_SQS_CONTAINER` | `wallet-service-localstack-1` (usado nos testes de queda) |
| `IT_LOG_LEVEL` | `error` (use `info` para ver os logs do serviço durante os testes) |

---

## 2. Build tag e comandos

Os testes de integração ficam atrás da **build tag `integration`**. Sem a tag,
o Go simplesmente não compila esses arquivos. Por isso `go test ./...` roda
só os testes rápidos e não precisa do Docker.

| O que rodar | Comando |
|---|---|
| Testes unitários | `go test ./...` |
| Unitários com detector de concorrência | `go test -race ./...` |
| Análise estática | `go vet ./...` |
| Integração | `go test -tags integration -count=1 -v ./test/integration/...` |
| Integração com detector de concorrência | `go test -race -tags integration -count=1 ./test/integration/...` |
| Um teste só | `go test -tags integration -count=1 -v -run NomeDoTeste ./test/integration/...` |
| Vários testes | `go test -tags integration -count=1 -v -run "TesteA\|TesteB" ./test/integration/...` |
| Análise estática incluindo os testes de integração | `go vet -tags integration ./...` |

O que cada opção faz:

- `-tags integration`: inclui os arquivos de integração;
- `-count=1`: obriga a rodar de novo (sem isso o Go pode reaproveitar um
  resultado antigo e mostrar `(cached)`);
- `-v`: mostra o nome e o resultado de cada teste;
- `-race`: liga o detector de concorrência;
- `-run`: escolhe quais testes rodar pelo nome.

O resultado esperado da integração é `--- PASS` em cada teste e, no final:

```
PASS
ok      wallet-service/test/integration
```

> No meio da saída pode aparecer `"level":"ERROR"` com `SERVICE_UNAVAILABLE`.
> **É esperado**: o `TestPostgresOutage` derruba o banco de propósito.

---

## 3. Múltiplas instâncias

O desafio pede que tudo continue correto com vários servidores rodando ao
mesmo tempo, cada um com sua memória e suas conexões.

### Teste automático

```bash
go test -tags integration -count=1 -v -run TestThreeInstances ./test/integration/...
```

O que ele faz:

1. compila o serviço e sobe **3 processos independentes**;
2. manda duas apostas de 80 sobre saldo de 100, **cada uma num servidor
   diferente**: uma passa e a outra é recusada por falta de saldo;
3. manda a mesma aposta 50 vezes, espalhada entre os 3 servidores: debita
   uma vez só;
4. movimenta várias carteiras ao mesmo tempo, por HTTP e pela fila, e confere
   que o saldo de todas bate com o extrato.

Outros testes que também usam processos separados:
`TestConsumerCrashAfterCommitIsRedelivered`, `TestCrashBetweenCommitAndPublish`,
`TestCrashBetweenPublishAndConfirm` e `TestRestartPreservesState`.

### Na mão

Suba 3 servidores (portas 8081, 8082 e 8083), todos usando o mesmo banco e a
mesma fila:

```bash
docker compose --profile multi up --build -d
```

Rode a demonstração apontando para cada um.

**Windows (PowerShell):**

```bash
$env:API="http://localhost:8082"; powershell -ExecutionPolicy Bypass -File scripts\demo.ps1
```

**Linux, Mac ou Git Bash:**

```bash
API=http://localhost:8082 ./scripts/demo.sh
```

Troque `8082` por `8081` ou `8083` para testar os outros. Para voltar ao
padrão no PowerShell: `Remove-Item Env:API`.

---

## 4. Simulações de falha

### Testes automáticos

| Falha simulada | Teste | Como simula |
|---|---|---|
| Servidor cai depois de gravar e antes de tirar a mensagem da fila | `TestConsumerCrashAfterCommitIsRedelivered` | derruba o processo no ponto exato |
| Servidor cai antes de publicar o evento | `TestCrashBetweenCommitAndPublish` | derruba o processo no ponto exato |
| Servidor cai depois de publicar e antes de confirmar | `TestCrashBetweenPublishAndConfirm` | derruba o processo no ponto exato |
| Reinício completo | `TestRestartPreservesState` | mata o processo e sobe outro |
| Banco de dados fora do ar | `TestPostgresOutage` | `docker pause` no Postgres |
| Fila fora do ar | `TestSQSOutage` | `docker pause` no LocalStack |
| Falha passageira (tenta de novo e funciona) | `TestSQSTransientFailureIsRetried` | erro injetado nas 2 primeiras tentativas |
| Falha que não passa (vai para a fila de erros) | `TestSQSExhaustedRetriesGoToDLQ` | erro injetado em todas as tentativas |

Para rodar todos de uma vez:

```bash
go test -tags integration -count=1 -v -run "Crash|Restart|Outage|Transient|Exhausted" ./test/integration/...
```

> Os testes de "fora do ar" usam o comando `docker pause` e precisam do Docker
> ligado. Sem o Docker, eles são pulados (`SKIP`) em vez de falhar.

### Na mão

Com o serviço no ar (`docker compose --profile multi up --build -d`):

**Derrubar um servidor de repente:**

```bash
docker compose kill app-2
```

Os outros continuam atendendo (rode a demonstração no 8081). Para religar:

```bash
docker compose --profile multi up -d
```

**Desligar um servidor com calma (como num deploy):**

```bash
docker compose stop app-3
```

Os logs mostram a ordem do desligamento (HTTP, fila, tarefas de fundo e, por
último, o banco):

```bash
docker compose logs app-3 --tail 10
```

**Banco de dados fora do ar:**

```bash
docker pause wallet-service-postgres-1
```

Abra http://localhost:8081/health/ready: aparece `"postgres":"DOWN"` com
código 503. As requisições respondem `503 SERVICE_UNAVAILABLE` em até 10
segundos, sem gravar nada pela metade. Para voltar:

```bash
docker unpause wallet-service-postgres-1
```

Em poucos segundos o health volta a `UP` e a demonstração funciona de novo.

**Fila fora do ar:**

```bash
docker pause wallet-service-localstack-1
```

O health mostra `"sqs":"DOWN"`, mas as operações por HTTP continuam
funcionando: os eventos ficam guardados no banco e são publicados quando a
fila volta. Para voltar:

```bash
docker unpause wallet-service-localstack-1
```

**Falha injetada no processamento (`FAULT_POINTS`):**

A variável `FAULT_POINTS` faz o serviço falhar de propósito em pontos
específicos. Ela existe só para testes e fica vazia no uso normal.

| Valor | Efeito |
|---|---|
| `wager.submit=fail:N` | as N primeiras operações falham com erro passageiro (503) |
| `wager.submit=fail` | todas as operações falham |
| `wager.after_commit=exit` | o processo morre logo depois de gravar uma operação |
| `consumer.after_commit=exit` | morre depois de gravar uma mensagem da fila, antes de tirá-la da fila |
| `outbox.after_publish=exit` | morre depois de publicar um evento, antes de marcá-lo como publicado |

Exemplo: faça as 2 primeiras operações falharem.

**Windows (PowerShell):**

```bash
$env:FAULT_POINTS="wager.submit=fail:2"; docker compose up -d app
```

**Linux, Mac ou Git Bash:**

```bash
FAULT_POINTS=wager.submit=fail:2 docker compose up -d app
```

Rode a demonstração 3 vezes: as duas primeiras param no passo 4 com
`SERVICE_UNAVAILABLE` (nada é gravado) e a terceira passa inteira.

Para tirar a falha, limpe a variável e recrie o servidor:

**Windows (PowerShell):**

```bash
Remove-Item Env:FAULT_POINTS; docker compose up -d app
```

**Linux, Mac ou Git Bash:**

```bash
docker compose up -d app
```

> Os valores com `exit` derrubam o processo, e o Docker o religa sozinho. Se
> a falha continuar ligada, ele cai de novo a cada operação. Esses cenários
> ficam mais claros nos testes automáticos, que controlam exatamente quando
> cada processo cai e sobe.

---

## 5. Desligar

```bash
docker compose --profile multi down
```

Para apagar também os dados do banco:

```bash
docker compose --profile multi down -v
```
