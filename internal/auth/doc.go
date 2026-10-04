// Package auth valida os tokens do Keycloak e diz quem está chamando.
//
// Os serviços pegam token com client_credentials. Conferimos assinatura
// (chaves do Keycloak), issuer, audience, expiração e se é access token.
//
// O providerId vem do claim provider_id, fixado no Keycloak para cada
// client. O corpo da requisição nunca define quem é o provedor: só pode
// repetir o que está no token.
//
// Papéis: wager-provider (provedores) e wallet-operator (serviço interno).
package auth
