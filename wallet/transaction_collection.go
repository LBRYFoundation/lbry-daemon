package wallet

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func BuildCollectionClaim(existing []byte, replace bool, fields map[string]any) ([]byte, error) {
	descriptor, err := claimV2MessageDescriptor()
	if err != nil {
		return nil, err
	}
	claim := dynamicpb.NewMessage(descriptor)
	if len(existing) > 0 && !replace {
		if existing[0] == 1 {
			if len(existing) < 85 {
				return nil, fmt.Errorf("%w: malformed signed collection", ErrInvalidClaimValue)
			}
			existing = append([]byte{0}, existing[85:]...)
		} else if existing[0] != 0 {
			return nil, fmt.Errorf("%w: unsupported collection wrapper", ErrInvalidClaimValue)
		}
		if err := proto.Unmarshal(existing[1:], claim); err != nil {
			return nil, err
		}
	}
	collectionField := descriptor.Fields().ByName("collection")
	collection := claim.Mutable(collectionField).Message()
	setOptionalProtoString(claim, "title", fields["title"])
	setOptionalProtoString(claim, "description", fields["description"])
	setSourceURL(claim, "thumbnail", fields["thumbnail_url"])
	if err := applyClaimLanguages(claim, fields); err != nil {
		return nil, err
	}
	if err := applyClaimLocations(claim, fields); err != nil {
		return nil, err
	}
	if transactionQueryBoolValue(fields["clear_tags"]) {
		claim.Clear(descriptor.Fields().ByName("tags"))
	}
	if tags, exists := fields["tags"]; exists && tags != nil {
		values, err := channelMutationStrings(tags)
		if err != nil {
			return nil, err
		}
		field := descriptor.Fields().ByName("tags")
		claim.Clear(field)
		list := claim.Mutable(field).List()
		for _, value := range values {
			list.Append(protoreflect.ValueOfString(value))
		}
	}
	referenceField := collection.Descriptor().Fields().ByName("claim_references")
	if transactionQueryBoolValue(fields["clear_claims"]) {
		collection.Clear(referenceField)
	}
	if references, exists := fields["claims"]; exists && references != nil {
		values, err := channelMutationStrings(references)
		if err != nil {
			return nil, err
		}
		if err := setClaimReferenceList(collection, values); err != nil {
			return nil, err
		}
	}
	encoded, err := marshalClaimProtoMessage(claim)
	if err != nil {
		return nil, err
	}
	return append([]byte{0}, encoded...), nil
}

func CreateCollectionTransaction(
	ctx context.Context, name string, amount uint64, address string,
	funding []*Account, fields map[string]any, channel *TransactionOutput,
) (*Transaction, error) {
	if len(funding) == 0 || funding[0] == nil {
		return nil, ErrPurchaseFundingAccount
	}
	claim, err := BuildCollectionClaim(nil, false, fields)
	if err != nil {
		return nil, err
	}
	if channel != nil {
		claim, err = placeholderSignedValue(claim, channel)
		if err != nil {
			return nil, err
		}
	}
	hash, err := transactionChangeAddressHash(address)
	if err != nil {
		return nil, err
	}
	transaction, err := CreateTransaction(ctx, nil, []TransactionOutput{
		NewClaimNameOutput(amount, name, claim, hash),
	}, funding, funding[0], channel == nil)
	if err != nil || channel == nil {
		return transaction, err
	}
	if err = finalizeSignedTransaction(ctx, transaction, funding, channel, false); err != nil {
		return nil, releaseFailedSignedTransaction(ctx, funding, transaction, err)
	}
	return transaction, nil
}

func CreateCollectionUpdateTransaction(
	ctx context.Context, previous *TransactionOutput, amount uint64, address string,
	funding []*Account, fields map[string]any, replace bool, channel *TransactionOutput,
) (*Transaction, error) {
	claim, err := BuildCollectionClaim(previous.Script.Claim, replace, fields)
	if err != nil {
		return nil, err
	}
	if channel != nil {
		claim, err = placeholderSignedValue(claim, channel)
		if err != nil {
			return nil, err
		}
	}
	hash, err := transactionChangeAddressHash(address)
	if err != nil {
		return nil, err
	}
	claimID, err := previous.ClaimID()
	if err != nil {
		return nil, err
	}
	output, err := NewUpdateClaimOutput(amount, string(previous.Script.ClaimName), claimID, claim, hash)
	if err != nil {
		return nil, err
	}
	input, err := NewSpendInput(previous)
	if err != nil {
		return nil, err
	}
	transaction, err := CreateTransaction(
		ctx, []TransactionInput{input}, []TransactionOutput{output}, funding, funding[0], channel == nil,
	)
	if err != nil || channel == nil {
		return transaction, err
	}
	if err = finalizeSignedTransaction(ctx, transaction, funding, channel, false); err != nil {
		return nil, releaseFailedSignedTransaction(ctx, funding, transaction, err)
	}
	return transaction, nil
}
