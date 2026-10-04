// Package app tem os casos de uso: junta domínio e banco por meio de
// interfaces. HTTP, SQS e o worker de pendências usam os mesmos serviços,
// então as regras são iguais em todos os canais.
//
// Comece por wager_submit.go: é o caminho completo de uma aposta.
package app
