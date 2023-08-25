package server

import (
	"net/http"

	"github.com/bitcoin-sv/go-paymail"
	"github.com/bitcoinschema/go-bitcoin/v2"
	"github.com/julienschmidt/httprouter"
	"github.com/libsv/go-bt/v2/bscript"
	apirouter "github.com/mrz1836/go-api-router"
)

/*
Incoming Data Object Example:
{
  "hex": "01000000012adda020db81f2155ebba69e7.........154888ac00000000",
  "metadata": {
	"sender": "someone@example.tld",
	"pubkey": "<sender-pubkey>",
	"signature": "signature(txid)",
	"note": "Human readable information related to the tx."
  },
  "reference": "someRefId"
}
*/

// p2pReceiveTx will receive a P2P transaction (from previous request: P2P Payment Destination)
//
// Specs: https://docs.moneybutton.com/docs/paymail-06-p2p-transactions.html
func (c *Configuration) p2pReceiveTx(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {

	// Get the params & paymail address submitted via URL request
	params := apirouter.GetParams(req)
	incomingPaymail := params.GetString("paymailAddress")

	// Start the P2PTransaction
	p2pTransaction := &paymail.P2PTransaction{
		Hex:       params.GetString("hex"),
		MetaData:  &paymail.P2PMetaData{},
		Reference: params.GetString("reference"),
	}

	// Parse the metadata JSON into the P2PTransaction struct
	metaData := params.GetJSON("metadata")
	if len(metaData) > 0 {
		// Parse the JSON object into p2pTransaction (little hackish)
		p2pTransaction.MetaData.Note, _ = metaData["note"].(string)
		p2pTransaction.MetaData.PubKey, _ = metaData["pubkey"].(string)
		p2pTransaction.MetaData.Sender, _ = metaData["sender"].(string)
		p2pTransaction.MetaData.Signature, _ = metaData["signature"].(string)
	}

	// Parse, sanitize and basic validation
	alias, domain, paymailAddress := paymail.SanitizePaymail(incomingPaymail)
	if len(paymailAddress) == 0 {
		ErrorResponse(w, req, ErrorInvalidParameter, "invalid paymail: "+incomingPaymail, http.StatusBadRequest)
		return
	} else if !c.IsAllowedDomain(domain) {
		ErrorResponse(w, req, ErrorUnknownDomain, "domain unknown: "+domain, http.StatusBadRequest)
		return
	}

	// Check for required fields
	if len(p2pTransaction.Hex) == 0 {
		ErrorResponse(w, req, ErrorMissingHex, "missing parameter: hex", http.StatusBadRequest)
		return
	} else if len(p2pTransaction.Reference) == 0 {
		ErrorResponse(w, req, ErrorMissingReference, "missing parameter: reference", http.StatusBadRequest)
		return
	}

	// Convert the raw tx into a transaction
	transaction, err := bitcoin.TxFromHex(p2pTransaction.Hex)
	if err != nil {
		ErrorResponse(w, req, ErrorInvalidParameter, "invalid parameter: hex", http.StatusBadRequest)
		return
	}

	// Start the final response
	response := &paymail.P2PTransactionPayload{
		Note: p2pTransaction.MetaData.Note,
		TxID: transaction.TxID(),
	}

	// Check signature if: 1) sender validation enabled or 2) a signature was given (optional)
	if c.SenderValidationEnabled || len(p2pTransaction.MetaData.Signature) > 0 {

		// Check required fields for signature validation
		if len(p2pTransaction.MetaData.Signature) == 0 {
			ErrorResponse(w, req, ErrorInvalidSignature, "missing parameter: signature", http.StatusBadRequest)
			return
		} else if len(p2pTransaction.MetaData.PubKey) == 0 {
			ErrorResponse(w, req, ErrorInvalidPubKey, "missing parameter: pubkey", http.StatusBadRequest)
			return
		}

		// Get the address from pubKey
		var rawAddress *bscript.Address
		if rawAddress, err = bitcoin.GetAddressFromPubKeyString(p2pTransaction.MetaData.PubKey, true); err != nil {
			ErrorResponse(w, req, ErrorInvalidPubKey, "invalid pubkey: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Validate the signature of the tx id
		if err = bitcoin.VerifyMessage(rawAddress.AddressString, p2pTransaction.MetaData.Signature, response.TxID); err != nil {
			ErrorResponse(w, req, ErrorInvalidSignature, "invalid signature: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	// Create the metadata struct
	md := CreateMetadata(req, alias, domain, "")

	// Get from the data layer
	var foundPaymail *paymail.AddressInformation
	foundPaymail, err = c.actions.GetPaymailByAlias(req.Context(), alias, domain, md)
	if err != nil {
		ErrorResponse(w, req, ErrorFindingPaymail, err.Error(), http.StatusExpectationFailed)
		return
	} else if foundPaymail == nil {
		ErrorResponse(w, req, ErrorPaymailNotFound, "paymail not found", http.StatusNotFound)
		return
	}

	// Record the transaction (verify, save, broadcast...)
	if response, err = c.actions.RecordTransaction(
		req.Context(), p2pTransaction, md,
	); err != nil {
		ErrorResponse(w, req, ErrorRecordingTx, err.Error(), http.StatusExpectationFailed)
		return
	}

	// Return the response
	apirouter.ReturnResponse(w, req, http.StatusOK, response)
}
