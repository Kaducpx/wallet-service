// Package sqs conecta o serviço ao SQS (LocalStack em ambiente local):
// consumidor de operações, publicador de eventos e monitor da DLQ.
//
// Regra principal do consumidor: a mensagem só sai da fila depois do commit.
package sqs
