// Package money representa valores monetários sem usar float em nenhum
// momento.
//
// O valor fica em centavos num int64 (25,00 = 2500), sempre junto da moeda.
// Parsing e formatação trabalham só com inteiros e strings.
//
// Limite: 92.233.720.368.547.758,07 (MaxInt64 centavos). Qualquer conta que
// passe disso devolve ErrAmountOverflow em vez de estourar em silêncio.
package money
