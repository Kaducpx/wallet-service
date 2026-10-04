#!/bin/bash
# Provisiona as filas no LocalStack (executado automaticamente quando o
# LocalStack fica pronto, via /etc/localstack/init/ready.d).
#
#   wager-transactions-dlq.fifo  DLQ da fila de entrada
#   wager-transactions.fifo      entrada de operações (redrive -> DLQ após 5 recebimentos)
#   wallet-events.fifo           destino dos eventos publicados pela outbox
set -euo pipefail

REGION="${AWS_DEFAULT_REGION:-us-east-1}"

awslocal sqs create-queue --region "$REGION" \
  --queue-name wager-transactions-dlq.fifo \
  --attributes FifoQueue=true,MessageRetentionPeriod=1209600

DLQ_URL=$(awslocal sqs get-queue-url --region "$REGION" --queue-name wager-transactions-dlq.fifo --query QueueUrl --output text)
DLQ_ARN=$(awslocal sqs get-queue-attributes --region "$REGION" --queue-url "$DLQ_URL" \
  --attribute-names QueueArn --query Attributes.QueueArn --output text)

# VisibilityTimeout (30s) > tempo máximo de processamento de uma mensagem.
# maxReceiveCount=5: após 5 recebimentos sem ack a mensagem vai para a DLQ.
cat > /tmp/wager-queue-attributes.json <<EOF
{
  "FifoQueue": "true",
  "VisibilityTimeout": "30",
  "MessageRetentionPeriod": "345600",
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"${DLQ_ARN}\",\"maxReceiveCount\":\"5\"}"
}
EOF
awslocal sqs create-queue --region "$REGION" \
  --queue-name wager-transactions.fifo \
  --attributes file:///tmp/wager-queue-attributes.json

awslocal sqs create-queue --region "$REGION" \
  --queue-name wallet-events.fifo \
  --attributes FifoQueue=true,MessageRetentionPeriod=1209600

# Política de acesso da fila de entrada: provedores (role wager-producer)
# apenas enviam; o serviço (role wallet-service) apenas consome. Na AWS real
# a política é aplicada pelo IAM; o LocalStack Community aceita e armazena a
# política, mas não a aplica (ver limitações no ARCHITECTURE.md).
REQ_URL=$(awslocal sqs get-queue-url --region "$REGION" --queue-name wager-transactions.fifo --query QueueUrl --output text)
REQ_ARN=$(awslocal sqs get-queue-attributes --region "$REGION" --queue-url "$REQ_URL" \
  --attribute-names QueueArn --query Attributes.QueueArn --output text)
POLICY=$(cat <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"ProducersSendOnly","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:role/wager-producer"},"Action":"sqs:SendMessage","Resource":"${REQ_ARN}"},
 {"Sid":"WalletServiceConsume","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:role/wallet-service"},"Action":["sqs:ReceiveMessage","sqs:DeleteMessage","sqs:ChangeMessageVisibility","sqs:GetQueueAttributes","sqs:GetQueueUrl"],"Resource":"${REQ_ARN}"}
]}
EOF
)
python3 - "$POLICY" > /tmp/wager-queue-policy.json <<'PY'
import json, sys
print(json.dumps({"Policy": json.dumps(json.loads(sys.argv[1]))}))
PY
awslocal sqs set-queue-attributes --region "$REGION" --queue-url "$REQ_URL" \
  --attributes file:///tmp/wager-queue-policy.json

echo "SQS queues provisioned"
