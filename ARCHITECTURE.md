# Arquitetura

Este documento registra as decisões técnicas, as interpretações adotadas e as
limitações conhecidas. Para executar o projeto, veja o [README](README.md).

## Sumário

1. [Visão geral](#1-visão-geral)
2. [Dinheiro](#2-dinheiro)
3. [Persistência e transações](#3-persistência-e-transações)
4. [Concorrência](#4-concorrência)
5. [Idempotência](#5-idempotência)
6. [Máquina de estados](#6-máquina-de-estados)
7. [Operações e reversões](#7-operações-e-reversões)
8. [Referências pendentes](#8-referências-pendentes)
9. [Códigos de falha](#9-códigos-de-falha)
10. [Invariantes impostas pelo banco](#10-invariantes-impostas-pelo-banco)
11. [Contrato HTTP](#11-contrato-http)
12. [Consumidor SQS e inbox](#12-consumidor-sqs-e-inbox)
13. [Outbox e eventos](#13-outbox-e-eventos)
14. [Autenticação e autorização](#14-autenticação-e-autorização)
15. [Composição com Fx e shutdown](#15-composição-com-fx-e-shutdown)
16. [Observabilidade](#16-observabilidade)
17. [Estratégia de testes](#17-estratégia-de-testes)
18. [Limitações, interpretações e trabalho não concluído](#18-limitações-interpretações-e-trabalho-não-concluído)

---

## 1. Visão geral

```
      HTTP (provedores / serviço interno)         SQS wager-transactions.fifo
                    │                                         │
            httpapi (auth, contrato)                 sqs.Consumer (inbox)
                    └──────────────┬──────────────────────────┘
                                   ▼
                        app.WagerService / WalletService      ◄── worker.PendingResolver
                                   │  (uma transação SQL por operação)
                                   ▼
          domain (Money, Wallet, WagerTransaction, Settle, eventos)
                                   │
                                   ▼
   PostgreSQL: wallets · wager_transactions · wallet_ledger_entries · inbox_messages · outbox_events
                                   │
                         worker.Relay (outbox) ──► SQS wallet-events.fifo
```

- `internal/domain` não depende de Fx, HTTP, SQS nem de pgx. Recebe valores
  tipados e devolve entidades, lançamentos e eventos. O value object de
  dinheiro fica em `internal/domain/money`, um pacote próprio e sem
  dependências.
- `internal/app` contém os casos de uso e as **portas** (interfaces). HTTP e
  SQS chamam o **mesmo** `WagerService.Submit`, então as garantias são as
  mesmas nos dois canais.
- `internal/adapters` implementa as portas (Postgres, SQS) e a API HTTP.
- `internal/worker` contém os processos de fundo; toda a coordenação entre
  instâncias acontece no banco, nunca em memória.

## 2. Dinheiro

**Representação.** `money.Money` (pacote `internal/domain/money`) é um value object imutável com
`int64` em **unidades mínimas** (centavos) e uma `Currency` validada. Nenhum
caminho converte para `float32`/`float64`: o parsing percorre a string decimal
dígito a dígito, a serialização monta a string com divisão inteira, e o JSON
carrega o valor como string (`{"amount":"25.00","currency":"BRL"}`).

**Escala e moedas.** A escala é fixa em 2 casas. São aceitas BRL, USD e EUR
(ISO 4217, todas com 2 casas de unidade mínima). O código precisa estar em
maiúsculas; `brl` é rejeitado, e não normalizado, para manter uma única forma
canônica. Os cenários principais usam BRL, e os testes cobrem incompatibilidade
entre moedas.

**Regras de entrada externa** (`money.Parse`):

- exatamente `\d+\.\d{2}`;
- sem sinal, espaços, expoente, `NaN` ou `Infinity`;
- sem zeros à esquerda supérfluos (`007.00` é rejeitado), garantindo uma
  representação canônica por valor;
- valores negativos são rejeitados;
- nada é arredondado: entrada fora do formato é erro. Como só existe uma forma
  aceita por valor, não há normalização de valor antes do hash de
  idempotência.

**Limites e overflow.** O maior valor é `92233720368547758.07`
(`math.MaxInt64` centavos). O menor valor interno é `-MaxInt64`, nunca
`MinInt64`, para que a negação seja sempre representável. Parsing, `Add`,
`Sub` e `Neg` retornam `ErrAmountOverflow` em vez de dar a volta. Um crédito
que estouraria o saldo vira a rejeição `BALANCE_LIMIT_EXCEEDED`.

**Aritmética.** `Add`, `Sub`, `Neg` e `Cmp` exigem a mesma moeda
(`ErrCurrencyMismatch`) e rejeitam valores não inicializados
(`ErrUninitializedMoney`). Valores negativos são permitidos em diferenças
internas (por exemplo, `difference` da reconciliação); quem impede saldo
negativo é a `Wallet` e o `CHECK` do banco.

**Persistência.** Colunas `*_units BIGINT` + `currency CHAR(3)`. Nenhuma coluna
usa `REAL`/`DOUBLE`. O valor e a moeda são preservados exatamente.

## 3. Persistência e transações

**Biblioteca.** `pgx/v5` com SQL explícito, sem ORM nem geração de código.
Locks, níveis de isolamento e constraints ficam visíveis no código
(`internal/adapters/postgres`).

**Delimitação da transação.** A porta `app.TxRunner` expõe:

- `InTx(ctx, fn)`: abre **uma** transação `READ COMMITTED`. Todos os
  repositórios usados dentro de `fn` (carteira, transação de aposta, ledger,
  inbox e outbox) recebem a mesma `pgx.Tx`. Ou tudo é confirmado no mesmo
  `COMMIT`, ou nada é.
- `ReadOnly(ctx, fn)`: `REPEATABLE READ READ ONLY`, um snapshot único, usado
  nas leituras e na reconciliação.

Os casos de uso nunca abrem conexões nem transações por conta própria; o
adaptador não decide regras de negócio.

**Contextos.** Toda operação de I/O recebe `context.Context`. O rollback usa
`context.WithoutCancel` para liberar locks mesmo se o contexto da requisição
já foi cancelado. Cada sessão tem `lock_timeout` (padrão 5s) e
`idle_in_transaction_session_timeout`.

**Prazos contra dependências travadas.** Um banco que para de responder sem
recusar conexões (por exemplo, congelado) faria as chamadas esperarem para
sempre. Por isso todo caminho tem prazo: cada requisição HTTP tem
`HTTP_REQUEST_TIMEOUT` (vencido: rollback e `503`), cada mensagem SQS tem 2/3
do visibility timeout (vencido: rollback e nova tentativa com backoff), e as
chamadas ao SQS (receber, publicar, readiness) têm prazos próprios. Isso é
verificado em `TestPostgresOutage` e `TestSQSOutage`, que congelam os
containers com `docker pause`.

**Classificação de erros** (`postgres.mapError`):

| Erro do Postgres | Classificação | Tratamento |
|---|---|---|
| `23505` unique_violation | `ErrUniqueViolation` | refaz a transação (disputa de chave) |
| `40001`, `40P01`, `55P03` | `ErrRetryable` | refaz a transação |
| classe `08`, `57P0x`, `53300`, falha de rede/timeout | `ErrUnavailable` | transitório: 503 / retry SQS |
| `pgx.ErrNoRows` | `ErrNotFound` | 404 / regra específica |
| CHECK/trigger de integridade | erro como está | bug ou tentativa de violar invariante |

## 4. Concorrência

**Estratégia: lock pessimista por carteira, com verificação otimista e
constraints como última barreira.**

1. `SELECT ... FROM wallets WHERE id = $1 FOR UPDATE`: lock de **linha** da
   carteira. Escritores da mesma carteira se serializam em qualquer instância;
   carteiras diferentes não se bloqueiam. Não existe lock global nem lock em
   memória.
2. `UPDATE wallets SET ... WHERE id = $1 AND version = $expected`: se 0 linhas
   forem afetadas, `ErrConcurrentUpdate`. Com o lock, isso nunca acontece; a
   verificação existe para que um caminho de código que esqueça o lock não
   provoque lost update silencioso.
3. O banco impõe uma única mudança por versão (`UNIQUE (wallet_id,
   wallet_version)` no ledger) e exige que cada mudança de saldo tenha o
   lançamento correspondente (constraint trigger no `COMMIT`).

**Ordem de locks.** Sempre carteira → transação (no `Submit` e no worker de
pendências). Cada operação toca uma única carteira, então não há ciclo e não
há deadlock por ordem de aquisição.

**Disputas por chave.** Duas requisições com a mesma chave idempotente para
carteiras diferentes não se serializam pelo lock da carteira. A segunda falha
no `UNIQUE` e é refeita (até 5 tentativas); ao refazer, encontra o registro
confirmado e responde com replay ou conflito. O mesmo vale para a disputa por
uma reversão (índice único parcial).

**Por que pessimista.** Operações financeiras curtas e com alta contenção na
mesma carteira (apostas da mesma rodada) favorecem o lock de linha: a segunda
transação espera alguns milissegundos em vez de abortar e refazer todo o
trabalho. Em carteiras independentes não há contenção.

**Evidência.** `TestTwoConcurrentBetsOf80` (80+80 sobre 100 → uma processada,
uma `INSUFFICIENT_FUNDS`, saldo 20.00, um débito), `TestSameBet50TimesInParallel`,
`TestDistinctWalletsProgressInParallel` (com a carteira A bloqueada, a
carteira B termina normalmente) e `TestThreeInstances` (os mesmos cenários
distribuídos entre três processos).

## 5. Idempotência

**Chaves.** Persistidas em `wager_transactions` com
`UNIQUE (provider_id, idempotency_key)` e `UNIQUE (provider_id,
external_transaction_id)`. Sobrevivem ao reinício de todos os processos.

- HTTP: header `Idempotency-Key`, obrigatório. A chave é usada como veio; o
  servidor nunca a substitui por uma calculada.
- SQS: `data.idempotencyKey`, com deduplicação adicional pela inbox (seção 12).

**Hash do payload.** SHA-256 (hex) do JSON canônico com chaves ordenadas
lexicograficamente e sem espaços. Campos incluídos (apenas negócio):

```
providerId, externalTransactionId, playerId, walletId, roundId, gameId, kind,
money.amount, money.currency, referenceExternalTransactionId (só quando informado)
```

Ficam fora do hash: `Idempotency-Key`/`idempotencyKey`, `messageId`,
`occurredAt`, `type` do envelope, headers e demais metadados de transporte.
Normalizações: UUIDs em minúsculas no formato canônico 8-4-4-4-12. O valor
monetário já chega na forma canônica (o parser só aceita uma forma). HTTP e
SQS usam a mesma função (`app.ParseWagerRequest`), portanto produzem o mesmo
hash; um teste unitário verifica isso.

**Semântica.**

| Situação | Resultado |
|---|---|
| Mesma chave, mesmo hash | resultado persistido, `idempotentReplay: true` (status original: 200 para PROCESSED, 202, 422…) |
| Mesma chave, hash diferente | `409 IDEMPOTENCY_KEY_CONFLICT` |
| Mesmo `(providerId, externalTransactionId)` com outra chave | `409 EXTERNAL_TRANSACTION_CONFLICT` (não reaplica) |

O replay devolve o **saldo observado no processamento original**
(`result_balance_units`), mesmo que a carteira tenha mudado depois. As chaves
são escopadas por provedor: a mesma chave em outro provedor é outra operação.
Assim, um provedor nunca recebe o resultado de outro.

## 6. Máquina de estados

```
PENDING ──┬──────────────────────────► PROCESSED (terminal)
          ├──────────────────────────► REJECTED  (terminal)
          ├──────────────────────────► FAILED    (terminal)
          └──► PENDING_REFERENCE ──┬──► PROCESSED
                 (retry/backoff)   ├──► REJECTED
                                   └──► FAILED
```

- As transições são métodos do domínio (`MarkProcessed`, `MarkRejected`,
  `MarkFailed`, `MarkPendingReference`, `ScheduleRetry`), validados por uma
  tabela única. Uma transação terminal não aceita novas transições, e o banco
  reforça isso com trigger.
- **PENDING nunca é confirmado.** O processamento é síncrono: a decisão e o
  efeito acontecem no mesmo commit. O `CHECK` de `status` nem admite
  `PENDING`. O único estado não terminal persistido é `PENDING_REFERENCE`, que
  tem retomada durável (seção 8). Portanto não existe "PENDING confirmado
  órfão".
- **Transitória × permanente.** Falhas de infraestrutura transitórias
  (conexão, timeout, deadlock, serialização, lock timeout) **não mudam o
  estado**: a transação SQL é desfeita e o chamador tenta de novo (HTTP 503 +
  `Retry-After`; SQS com backoff). `FAILED` é reservado a falhas
  **permanentes** registradas para auditoria: hoje, quando o worker de
  pendências encontra um erro não transitório (invariante violada ou dado
  inconsistente) ao retomar uma operação. Nesse caso ele grava `FAILED` com
  `PERMANENT_PROCESSING_ERROR` em vez de tentar para sempre.
- **Reidratação** (`RehydrateTransaction`, `RehydrateWallet`) apenas valida e
  reconstrói o estado: não executa transições, não movimenta saldo e não emite
  eventos.

## 7. Operações e reversões

| Tipo | Movimento | Regras |
|---|---|---|
| BET | débito | valor > 0; saldo suficiente, senão `INSUFFICIENT_FUNDS` |
| WIN | crédito | valor > 0; referência opcional (deve ser uma BET da mesma rodada) |
| LOSS | nenhum | valor exatamente `0.00`; sem ledger, sem mudança de versão; emite só `WagerTransactionProcessed` |
| REFUND | crédito | referência obrigatória a uma BET PROCESSED; valor igual |
| ROLLBACK | contrário ao original | referência obrigatória a BET (crédito), WIN ou REFUND (débito) PROCESSED; valor igual |
| OPENING | crédito | apenas interno (abertura); rejeitado via HTTP/SQS |

A operação e a referência precisam concordar em provedor (a busca é feita por
`(providerId, referenceExternalTransactionId)`), jogador, carteira, moeda e
rodada; senão, `REFERENCE_MISMATCH`. Reversões são sempre integrais
(`REFERENCE_AMOUNT_MISMATCH` caso contrário).

**Combinações de REFUND e ROLLBACK sobre a mesma aposta.** A política
adotada: **cada transação pode receber no máximo uma reversão bem-sucedida,
seja REFUND ou ROLLBACK.** Ela é imposta pelo índice único parcial
`wager_single_successful_reversal (reference_transaction_id) WHERE status =
'PROCESSED' AND kind IN ('REFUND','ROLLBACK')` e verificada antes pelo domínio
(`REFERENCE_ALREADY_REVERSED`). Consequências:

- REFUND após REFUND, ROLLBACK após ROLLBACK, ROLLBACK após REFUND e REFUND
  após ROLLBACK da mesma BET são rejeitados. O débito da aposta é devolvido no
  máximo uma vez.
- Um ROLLBACK **de um REFUND** é permitido (ele reverte o REFUND, que é outra
  transação) e debita o valor devolvido. Isso não "reabre" a aposta para um
  novo REFUND: a BET continua com a reversão original registrada. É uma
  escolha conservadora, pois impede ciclos de devolução.
- ROLLBACK de LOSS ou de ROLLBACK é rejeitado (`REFERENCE_INVALID_KIND`).

**Reversão sem saldo.** Um ROLLBACK que precisaria debitar mais do que o saldo
disponível (de um WIN ou REFUND) é rejeitado com
`REVERSAL_INSUFFICIENT_FUNDS`, código diferente do `INSUFFICIENT_FUNDS` da
BET. Ele fica persistido como REJECTED, com evento de rejeição, e é
consultável.

**Valores zero.** Saldo inicial zero é aceito (sem OPENING, ledger ou
eventos). LOSS exige zero. BET, WIN, REFUND e ROLLBACK exigem valor > 0. A
política é aplicada no parsing, no domínio e no `CHECK wager_amount_policy`.

## 8. Referências pendentes

Quando uma operação depende de uma referência (REFUND/ROLLBACK sempre; WIN
quando a informa) que ainda não chegou, ela é gravada como
`PENDING_REFERENCE`, com `attempts = 0`, `next_retry_at = now + backoff` e
`expires_at = now + PENDING_TTL`. Um evento `WagerTransactionPendingReference`
é emitido no mesmo commit. O HTTP responde `202`; no SQS, a mensagem é
concluída (inbox gravada) e removida, e o worker assume a continuidade.

**Worker (`worker.PendingResolver`).** Em cada ciclo:

1. `ClaimDue` reserva até N operações vencidas com `FOR UPDATE SKIP LOCKED`,
   empurrando `next_retry_at` por um lease (padrão 30s). Instâncias diferentes
   pegam itens diferentes. Se a instância cair, o lease vence e outra assume.
   Como o estado vive no banco, a retomada sobrevive a reinícios.
2. Para cada item: bloqueia a carteira, bloqueia e relê a transação. Se ela
   não estiver mais pendente, não faz nada. Caso contrário, busca a referência
   e decide pelo domínio (`Settle`).
3. Referência ainda ausente: `attempts++` e `next_retry_at` com backoff
   exponencial (base 1s, dobra a cada tentativa, teto 1min, jitter de até
   20%).
4. TTL vencido **ou** `PENDING_MAX_ATTEMPTS` atingido: `REJECTED` com
   `REFERENCE_NOT_FOUND` e evento `WagerTransactionRejected`.

Quando a referência chega e é processada, as operações que a esperam têm o
`next_retry_at` antecipado para "agora" no mesmo commit; a resolução acontece
no próximo ciclo do worker, sem esperar o backoff.

**Referência existe, mas…**

- ainda está `PENDING_REFERENCE` (ex.: ROLLBACK de um REFUND que espera a BET):
  a operação continua esperando, sujeita ao mesmo TTL;
- terminou `REJECTED` ou `FAILED`: rejeição definitiva com
  `REFERENCE_NOT_PROCESSED`.

## 9. Códigos de falha

**Resultados definitivos** (persistidos; o replay devolve sempre o mesmo
código; HTTP `422` com o corpo da transação):

| failureCode | Quando |
|---|---|
| `INSUFFICIENT_FUNDS` | BET sem saldo |
| `REVERSAL_INSUFFICIENT_FUNDS` | ROLLBACK que deixaria o saldo negativo |
| `WALLET_PLAYER_MISMATCH` | carteira de outro jogador |
| `WALLET_CURRENCY_MISMATCH` | moeda diferente da carteira |
| `REFERENCE_NOT_FOUND` | referência não chegou dentro do TTL/tentativas |
| `REFERENCE_NOT_PROCESSED` | referência terminou REJECTED/FAILED |
| `REFERENCE_MISMATCH` | referência de outro jogador, carteira, moeda ou rodada |
| `REFERENCE_INVALID_KIND` | tipo de referência incompatível |
| `REFERENCE_AMOUNT_MISMATCH` | reversão com valor diferente do original |
| `REFERENCE_ALREADY_REVERSED` | a referência já foi revertida |
| `BALANCE_LIMIT_EXCEEDED` | crédito estouraria o limite representável |
| `PERMANENT_PROCESSING_ERROR` | (status FAILED) falha permanente registrada para auditoria |

**Entradas corrigíveis** (nada é persistido; o cliente pode corrigir e
reenviar com a mesma chave; HTTP `400`): `INVALID_REQUEST`, `INVALID_FIELD`,
`INVALID_MONEY`, `UNSUPPORTED_CURRENCY`, `INVALID_KIND`, `OPENING_NOT_ALLOWED`,
`INVALID_AMOUNT_FOR_KIND`, `MISSING_REFERENCE` e `MISSING_IDEMPOTENCY_KEY`.

## 10. Invariantes impostas pelo banco

Todas valem mesmo para quem escreve direto no banco (verificadas em
`TestDatabaseInvariants`):

| Invariante | Mecanismo |
|---|---|
| Saldo ≥ 0 | `CHECK wallets_balance_non_negative` |
| Uma carteira por (jogador, moeda) | `UNIQUE (player_id, currency)` |
| Versão só muda com o saldo, sempre +1 | trigger `wallets_guard` |
| Toda mudança de saldo tem o lançamento correspondente | constraint trigger `DEFERRABLE` `wallets_require_ledger` (no COMMIT) |
| Ledger append-only | triggers bloqueiam UPDATE, DELETE e TRUNCATE; o papel `wallet_app` só tem SELECT/INSERT |
| Aritmética do lançamento | `CHECK ledger_arithmetic` (after = before ± amount) |
| Um lançamento por (carteira, transação) e por versão | `UNIQUE (wallet_id, transaction_id)`, `UNIQUE (wallet_id, wallet_version)` |
| Moeda do lançamento = moeda da carteira | FK composta `(wallet_id, currency)` |
| Lançamento só para transação PROCESSED que movimenta saldo | constraint trigger `ledger_requires_processed_transaction` |
| Idempotência | `UNIQUE (provider_id, idempotency_key)`, `UNIQUE (provider_id, external_transaction_id)` |
| Crédito inicial único | índice único parcial `kind = 'OPENING'` |
| Uma reversão bem-sucedida por referência | índice único parcial `wager_single_successful_reversal` |
| Formato interno × externo | `CHECK wager_origin_shape` (OPENING sem provedor, ids, rodada, jogo ou referência) |
| Política de valores por tipo | `CHECK wager_amount_policy` |
| Transação terminal imutável; colunas financeiras imutáveis | trigger `wager_transactions_guard` + GRANT por coluna |
| Snapshot da outbox imutável | trigger `outbox_guard` |
| Deduplicação de mensagens | `PRIMARY KEY (consumer_name, message_id)` |

**Papéis.** As migrations rodam com `wallet_owner`. A aplicação conecta com
`wallet_app`, que não tem `DELETE` nem `TRUNCATE` em nenhuma tabela e tem
`UPDATE` apenas nas colunas mutáveis (saldo/versão, estado da transação,
controle da outbox).

## 11. Contrato HTTP

| Rota | Quem pode |
|---|---|
| `POST /wallets` | `wallet-operator` |
| `GET /wallets/{walletId}` | `wallet-operator` |
| `GET /wallets/{walletId}/ledger?cursor=&limit=` | `wallet-operator` |
| `POST /wallets/{walletId}/reconciliation` | `wallet-operator` |
| `POST /wagering/transactions` | `wager-provider` (somente com o próprio `providerId`) |
| `GET /wagering/transactions/{transactionId}` | operador (qualquer) ou provedor (somente as próprias) |
| `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}` | operador ou o próprio provedor |
| `GET /health/live`, `GET /health/ready`, `GET /metrics` | público |

**`POST /wagering/transactions`.** Corpo da resposta:

```json
{ "transactionId": "…", "status": "PROCESSED", "balance": {"amount":"975.00","currency":"BRL"},
  "failureCode": "INSUFFICIENT_FUNDS", "idempotentReplay": false }
```

| Status HTTP | Significado |
|---|---|
| `201` | PROCESSED (primeiro processamento) |
| `200` | PROCESSED (replay) |
| `202` | PENDING_REFERENCE: acompanhar via GET |
| `422` | REJECTED: regra de negócio, `failureCode` no corpo |
| `500` | FAILED (permanente) ou erro interno |
| `400` | entrada inválida (corrigível) |
| `401` | token ausente, inválido ou expirado |
| `403` | sem permissão; `PROVIDER_MISMATCH` quando o corpo não é do provedor do token |
| `404` | `WALLET_NOT_FOUND` / `TRANSACTION_NOT_FOUND` |
| `409` | `IDEMPOTENCY_KEY_CONFLICT`, `EXTERNAL_TRANSACTION_CONFLICT`, `WALLET_ALREADY_EXISTS` |
| `503` | indisponibilidade transitória (`SERVICE_UNAVAILABLE`, com `Retry-After: 1`) |

Erros fora do formato de transação usam sempre:

```json
{ "error": { "code": "INVALID_MONEY", "message": "…", "field": "money.amount" } }
```

**Ledger.** Ordenado por `walletVersion` (única por carteira, portanto
ordenação estável). O cursor é opaco (base64url de `{"v": última versão}`);
`limit` vai de 1 a 200, padrão 50.

**Reconciliação.** Lê saldo e soma do ledger no mesmo snapshot `REPEATABLE
READ`. `difference = armazenado − reconstruído`. Uma divergência aparece na
resposta (`consistent: false`), num log `ERROR` e na métrica
`wallet_reconciliation_divergences_total`. A reconciliação nunca altera
saldo.

**Corpo estrito.** Campos desconhecidos são rejeitados, o corpo é limitado a
64 KiB e não pode haver conteúdo após o JSON.

## 12. Consumidor SQS e inbox

**Fluxo por mensagem:**

1. Decodifica o envelope e valida `data` com a **mesma** função do HTTP.
2. Chama `WagerService.Submit` com um registro de inbox. Na **mesma
   transação SQL** são gravados a inbox `(consumerName, messageId, hash)`, a
   transação de aposta, o saldo, o ledger e a outbox.
3. Só **depois do commit** chama `DeleteMessage`.

Se o processo cair entre 2 e 3, a mensagem volta após o visibility timeout. A
inbox reconhece o `messageId` (e confere o hash) e a mensagem é removida sem
novo efeito. Isso é validado em `TestConsumerCrashAfterCommitIsRedelivered`
com um processo que morre de fato (`os.Exit`) nesse ponto. Uma reentrega com
o **mesmo messageId e conteúdo diferente** vai para a DLQ (`conflict`).

**Decisões por resultado:**

| Resultado | Ação |
|---|---|
| Processada, rejeitada por regra de negócio, pendente ou duplicada | commit + `DeleteMessage` |
| JSON/envelope inválido, tipo desconhecido, validação de domínio (incl. OPENING) | envio explícito à DLQ (`failureReason=invalid_message`) + delete |
| Conflito de idempotência ou de inbox | DLQ (`conflict`) |
| Carteira inexistente | DLQ (`wallet_not_found`) |
| Transitório ou desconhecido | `ChangeMessageVisibility` com backoff exponencial (`SQS_RETRY_BASE` × 2^(n−1), teto `SQS_RETRY_MAX`); após `maxReceiveCount` (5) a redrive policy move para a DLQ |

**Limites.** Visibility timeout de 30s (bem acima do tempo de processamento
de uma mensagem, que é de milissegundos). `maxReceiveCount = 5`. Long polling
de 10s, lotes de até 10 mensagens, 4 loops de recebimento por instância.
O processamento de cada mensagem tem prazo de 2/3 do visibility timeout:
mesmo com o banco travado, a transação desiste antes que o SQS reentregue
a mensagem ainda em andamento.

**MessageGroupId e MessageDeduplicationId (contrato para os produtores).**

- `MessageGroupId = walletId`: operações da mesma carteira chegam em ordem e
  não ficam em processamento simultâneo no consumidor; carteiras diferentes
  são processadas em paralelo, inclusive entre instâncias. Dentro de um lote,
  mensagens do mesmo grupo são tratadas em sequência; se uma falhar de forma
  transitória, as seguintes do grupo são devolvidas sem processamento para
  não passarem à frente.
- `MessageDeduplicationId = messageId` do envelope. A deduplicação do SQS
  FIFO (janela de 5 minutos) é apenas uma otimização: a correção depende da
  inbox e das chaves idempotentes, persistidas no banco.

**HTTP × SQS.** Os dois canais usam o mesmo caso de uso e o mesmo hash, então
a mesma operação recebida por ambos gera um único efeito, em qualquer ordem e
inclusive em paralelo (`TestHTTPAndSQSSameOperation`).

**Shutdown (SIGTERM).** O consumidor cancela imediatamente o long polling
(para de buscar trabalho). As mensagens em andamento terminam dentro do prazo
de encerramento. Se o prazo vencer, o contexto de trabalho é cancelado (a
transação SQL faz rollback) e as mensagens não concluídas recebem
`ChangeMessageVisibility(0)` para reentrega imediata e segura.

## 13. Outbox e eventos

**Gravação.** Os eventos são serializados (envelope completo) **dentro do
commit** que os origina e gravados em `outbox_events.payload` (tipo `JSON`,
que preserva os bytes exatos). Nenhum evento é publicado antes do commit: o
publicador só enxerga linhas confirmadas.

**Publicação (`worker.Relay`).**

1. `Claim`: `UPDATE … WHERE id IN (SELECT … WHERE published_at IS NULL AND
   next_attempt_at <= now() AND (locked_until IS NULL OR locked_until < now())
   ORDER BY occurred_at LIMIT n FOR UPDATE SKIP LOCKED)`. Grava `locked_by`,
   `locked_until = now + lease` e incrementa `attempts`. Vários publicadores
   pegam lotes disjuntos sem se bloquear.
2. `SendMessage` para `wallet-events.fifo`.
3. `MarkPublished` somente se o lease ainda pertence a este publicador.
4. Falha no envio: `next_attempt_at` com backoff exponencial
   (`OUTBOX_RETRY_BASE`…`OUTBOX_RETRY_MAX`) e `last_error`.

**Recuperação.**

- *Queda entre o commit e a publicação*: o evento continua pendente e
  qualquer instância o publica (`TestCrashBetweenCommitAndPublish`).
- *Queda entre a publicação e a confirmação*: o lease vence e outra instância
  republica o **mesmo payload com o mesmo eventId**
  (`TestCrashBetweenPublishAndConfirm`).
- A entrega é *at-least-once*. Consumidores devem deduplicar por `eventId`.
  Dentro de 5 minutos, o próprio SQS FIFO descarta a duplicata
  (`MessageDeduplicationId = eventId`).
- No encerramento, o restante do lote reservado é liberado na hora, sem
  esperar o lease vencer.

**Contrato de roteamento (`wallet-events.fifo`).**

- `MessageGroupId = aggregateId` (walletId); `MessageDeduplicationId = eventId`.
- Atributos de mensagem: `eventType`, `eventId`, `correlationId`.
- Corpo = envelope:

```json
{
  "eventId": "uuid", "eventType": "WalletBalanceChanged", "aggregateId": "walletId",
  "correlationId": "…", "causationId": "…(opcional)", "occurredAt": "2026-09-08T12:00:00Z",
  "version": 1,
  "data": { "walletId": "…", "transactionId": "…", "direction": "DEBIT",
            "money": {"amount":"25.00","currency":"BRL"},
            "balanceBefore": {"amount":"1000.00","currency":"BRL"},
            "balanceAfter": {"amount":"975.00","currency":"BRL"}, "walletVersion": 2 }
}
```

| eventType | Gatilho | data |
|---|---|---|
| `WagerTransactionProcessed` | conclusão bem-sucedida (inclui LOSS e OPENING) | transactionId, walletId, playerId, origin, kind, money, balanceAfter, providerId?, externalTransactionId?, roundId?, gameId?, referenceTransactionId?, processedAt |
| `WagerTransactionRejected` | rejeição definitiva | transactionId, walletId, playerId, kind, money, failureCode, providerId, externalTransactionId, roundId, gameId, rejectedAt |
| `WalletBalanceChanged` | mudança efetiva de saldo | walletId, transactionId, direction, money, balanceBefore, balanceAfter, walletVersion |
| `WagerTransactionPendingReference` | entrada em espera | transactionId, walletId, playerId, kind, money, providerId, externalTransactionId, referenceExternalTransactionId, expiresAt |

Tipo e versão são definidos pelo tipo concreto do evento (seu construtor),
nunca por quem publica. Timestamps em UTC (RFC 3339), dinheiro como string
decimal. `correlationId` vem do `X-Correlation-Id` (HTTP) ou do atributo
`correlationId`/`messageId` (SQS). `causationId` é o `messageId` (SQS) ou o id
da transação que causou o evento.

**Ordem.** Para uma carteira, os eventos de uma mesma transação são gravados
na ordem lógica. Com vários publicadores simultâneos, eventos da mesma
carteira podem ser enviados fora da ordem de ocorrência. Consumidores devem
usar `walletVersion` (em `WalletBalanceChanged`) para ordenar o saldo e
`eventId` para deduplicar.

## 14. Autenticação e autorização

**IdP.** Keycloak 26, executado no Docker Compose, com realm provisionado
automaticamente por import (`deploy/keycloak/realm-wallet.json`). A escolha se
deve a ser OIDC completo, open source, com suporte nativo a
`client_credentials`, roles e mapeadores de claims, e por rodar localmente
sem dependências externas. Cadastro de senhas e emissão própria de tokens
estão fora do escopo: o serviço apenas valida tokens.

**Fluxo.** Serviços (provedores e o serviço interno) obtêm tokens via OAuth
2.0 `client_credentials`. O serviço valida cada requisição (`internal/auth`,
com `go-oidc`):

- assinatura RS256 com as chaves do JWKS do Keycloak (buscadas sob demanda e
  cacheadas; a rotação é tratada pela biblioteca);
- `iss` igual a `OIDC_ISSUER`; `aud` contém `wallet-service` (audience mapper
  por client); `exp` válido;
- `typ = Bearer` (ID tokens não são aceitos).

`OIDC_ISSUER` e `OIDC_JWKS_URL` são configurados separadamente porque, no
Compose, o issuer público (`http://localhost:8080/...`) difere do endereço
interno (`http://keycloak:8080/...`). O Keycloak roda com `KC_HOSTNAME`
fixo para que o `iss` seja o mesmo independentemente de onde o token foi
pedido.

**Modelo de permissões.**

| Realm role | Quem | Pode |
|---|---|---|
| `wager-provider` | clients de provedores | enviar operações e consultar as **próprias** transações |
| `wallet-operator` | serviço interno (`wallet-backoffice`) | abrir carteiras, consultar carteira/ledger, reconciliar, consultar qualquer transação |

- **A identidade determina o providerId.** O claim `provider_id` é fixado
  por client no IdP (hardcoded claim mapper). O corpo só pode **repetir** esse
  valor: se divergir, `403 PROVIDER_MISMATCH` antes de qualquer acesso ao
  banco, sem efeito financeiro e sem revelar dados.
- **Isolamento em consultas e replays.** As chaves idempotentes são escopadas
  por provedor, então um provedor nunca recebe o replay de outro. Um provedor
  que consulta pelo id interno uma transação de outro provedor (ou um
  OPENING) recebe `404`, sem revelar se o id existe. A rota
  `/providers/{providerId}/…` exige que `{providerId}` seja o do token.
- **Operações de carteira** são restritas ao `wallet-operator`. Provedores e
  clients sem papel recebem `403`.
- Token ausente, inválido, adulterado ou expirado gera `401` com
  `WWW-Authenticate`. O motivo detalhado vai para o log, nunca o token.

**Mensageria.** No SQS a identidade vem das credenciais AWS e da política da
fila. O provisionamento aplica à `wager-transactions.fifo` uma política em
que produtores (`role/wager-producer`) apenas enviam e o serviço
(`role/wallet-service`) apenas consome. O consumidor **mantém todas as
validações de domínio**: formato, moeda, carteira, jogador, referência do
mesmo provedor e idempotência. Veja a limitação do LocalStack na seção 18.

## 15. Composição com Fx e shutdown

**Módulos** (`internal/bootstrap`): `config`, `logging`, `metrics`,
`postgres`, `sqs`, `auth`, `app`, `workers` e `http`. Todos são compostos com
`fx.Module`/`fx.Provide`/`fx.Invoke` e injeção por construtor. O domínio não
conhece o Fx.

**Inicialização.** A configuração é validada no construtor (configuração
inválida impede o start). `OnStart` valida as dependências com retry até o
`StartTimeout` (60s): `Ping` no Postgres, resolução das três filas no SQS e
acesso ao JWKS do IdP. O servidor HTTP faz `Listen` síncrono (porta ocupada
falha o start) e é o último a iniciar.

**Encerramento** (`SIGINT`/`SIGTERM`, prazo `SHUTDOWN_TIMEOUT`): o Fx executa
os `OnStop` na ordem inversa da inicialização.

1. **HTTP**: readiness passa a responder 503, `Shutdown` recusa novas
   conexões e espera as requisições em andamento.
2. **Consumidor SQS**: para de buscar, conclui as mensagens em andamento ou
   devolve a visibilidade (seção 12).
3. **Worker de pendências** e **publicador da outbox**: não iniciam novos
   ciclos; o ciclo corrente termina dentro do prazo e depois é cancelado. O
   publicador libera o restante do lote reservado.
4. **IdP, SQS e pool do Postgres**: o pool é fechado por último, depois de
   todos os componentes que o usam.

Cada worker expõe `Done()`, fechado quando suas goroutines terminam de fato
(término observável). `TestFxLifecycle` verifica que, após o `Stop`, as
goroutines terminaram, o pool está fechado e a porta foi liberada.
`TestGraphIsValid` valida o grafo de dependências sem infraestrutura.

## 16. Observabilidade

- Logs JSON (`log/slog`) com `instanceId` e, quando disponíveis,
  `correlationId`, `messageId`, `transactionId`, `walletId` e `providerId`. Os
  eventos do próprio Fx vão para o mesmo logger. Não são registrados tokens,
  segredos nem payloads financeiros completos (apenas ids, status e códigos).
- Métricas Prometheus (registry por instância): resultados por status e
  canal, duplicatas (por chave e por inbox), conflitos de concorrência,
  retries do SQS, envios e profundidade da DLQ (`wallet_sqs_dlq_depth`, que inclui as
  mensagens movidas pela redrive policy), atraso e tamanho da outbox, retries de pendências,
  latência de processamento e HTTP, falhas de autenticação e divergências de
  reconciliação. A lista completa está no README.
- Health checks: `/health/live` (processo) e `/health/ready` (Postgres + SQS;
  503 durante o shutdown).

## 17. Estratégia de testes

| Camada | Onde | O que cobre |
|---|---|---|
| Unitários | `internal/domain` | Money (parsing, escala, limites, overflow, NaN/Infinity/notação científica, moedas incompatíveis, JSON), invariantes da carteira, ledger, máquina de estados, regras dos cinco tipos, política de zero, abertura interna com metadados e eventos, envelope |
| Unitários | `internal/app` | validação de entrada, hash canônico (equivalência HTTP×SQS), conflito de payload para a mesma chave |
| Unitários | `internal/bootstrap` | grafo do Fx e rejeição de configuração inválida |
| Integração | `test/integration` (tag `integration`) | Postgres, Keycloak e LocalStack reais: migrations up/down/up, constraints e imutabilidade, atomicidade, auth (ausente/inválido/adulterado/expirado), isolamento entre provedores, concorrência (80+80, 50×, carteiras distintas), idempotência, reversões e pendências (resolução e expiração), inbox e reentrega, HTTP×SQS, DLQ (inválidas e tentativas esgotadas), retry transitório, outbox concorrente e recuperação, reinício, 3 processos independentes, PostgreSQL e SQS fora do ar (`docker pause`), profundidade da DLQ e ciclo de vida do Fx |

Todos os cenários terminam conferindo **saldo armazenado = créditos − débitos
do ledger** (direto no banco e pelo endpoint de reconciliação) e versão =
número de lançamentos. A suíte inteira passa com `go test -race`.

## 18. Limitações, interpretações e trabalho não concluído

**Limitações conhecidas**

- **Política da fila no LocalStack.** O LocalStack Community armazena a
  política de acesso da fila, mas não a aplica (a aplicação de IAM é recurso
  pago). Localmente qualquer credencial acessa as filas. Em AWS real a
  política e os papéis IAM controlariam produtores e consumidor. As
  validações de domínio no consumidor valem em ambos os casos.
- **Ordem de eventos com vários publicadores.** Garantida por carteira
  apenas com um publicador. Com vários, os consumidores devem usar
  `walletVersion` e `eventId` (seção 13).
- **Retenção da outbox e da inbox.** Não há expurgo de linhas antigas (o
  papel da aplicação nem tem `DELETE`). Em produção haveria um job de
  arquivamento com um papel próprio.
- **`/metrics` público** na mesma porta da API. Em produção ficaria em porta
  interna ou atrás de autenticação.
- **Lease das pendências.** Se uma instância cair logo após reservar
  pendências, elas só são retomadas quando o lease vence (padrão 30s).
- **Encerramento gracioso de subprocessos no Windows.** Os testes
  multi-processo usam `Kill` (queda abrupta, que é o objetivo desses
  cenários). O shutdown gracioso é testado com a aplicação Fx no mesmo
  processo e pode ser observado com `docker compose stop`.
- **Escala fixa de 2 casas.** Moedas com outra unidade mínima (ex.: JPY,
  BHD) não são suportadas.

**Interpretações adotadas**

- Abertura com saldo positivo: a carteira já nasce na versão 1, e o
  lançamento de OPENING registra 0.00 → saldo inicial com `walletVersion = 1`.
- Carteira inexistente não gera transação persistida (a transação exige FK
  para a carteira): `404` no HTTP e DLQ (`wallet_not_found`) no SQS.
- Divergência de jogador ou moeda entre a operação e a carteira é rejeição
  de negócio **persistida** (resultado definitivo), não erro de entrada.
- WIN com referência também espera a referência (`PENDING_REFERENCE`), com
  as mesmas regras de TTL das reversões.
- Uma referência recebe no máximo **uma** reversão bem-sucedida, de qualquer
  tipo (seção 7). É mais restritivo que "uma de cada tipo" e impede devolução
  dupla.
- Chaves idempotentes e ids externos são escopados por provedor.
- Rejeições de `REFERENCE_NOT_FOUND` não carregam saldo observado, porque a
  carteira não foi tocada pela operação.

**Não concluído / diferenciais não implementados**

- Ledger de partidas dobradas (opcional).
- Tracing com OpenTelemetry e dashboards (opcionais).
- Testes de carga com relatório de throughput/percentis (opcionais).
