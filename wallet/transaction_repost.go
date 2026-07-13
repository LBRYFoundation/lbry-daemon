package wallet

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func BuildRepostClaim(claimID string, fields map[string]any) ([]byte, error) {
	descriptor, err := claimV2MessageDescriptor()
	if err != nil {
		return nil, err
	}
	claim := dynamicpb.NewMessage(descriptor)
	repostField := descriptor.Fields().ByName("repost")
	repost := claim.Mutable(repostField).Message()
	if err := setClaimReference(repost, claimID); err != nil {
		return nil, err
	}
	setOptionalProtoString(claim, "title", fields["title"])
	setOptionalProtoString(claim, "description", fields["description"])
	setSourceURL(claim, "thumbnail", fields["thumbnail_url"])
	if raw, ok := fields["tags"]; ok && raw != nil {
		values, err := channelMutationStrings(raw)
		if err != nil {
			return nil, err
		}
		list := claim.Mutable(descriptor.Fields().ByName("tags")).List()
		for _, value := range values {
			list.Append(protoreflect.ValueOfString(value))
		}
	}
	encoded, err := marshalClaimProtoMessage(claim)
	if err != nil {
		return nil, err
	}
	return append([]byte{0}, encoded...), nil
}

func setClaimReference(message protoreflect.Message, claimID string) error {
	if len(claimID) != 40 {
		return fmt.Errorf("Invalid claim id. It is expected to be a 40 characters long hexadecimal string.")
	}
	descriptor := message.Descriptor()
	container := dynamicpb.NewMessage(descriptor.ParentFile().Messages().ByName("ClaimList"))
	if err := setClaimReferenceList(container, []string{claimID}); err != nil {
		return fmt.Errorf("Invalid claim id. It is expected to be a 40 characters long hexadecimal string.")
	}
	references := container.Get(container.Descriptor().Fields().ByName("claim_references")).List()
	if references.Len() != 1 {
		return fmt.Errorf("invalid repost claim reference")
	}
	field := descriptor.Fields().ByName("claim_hash")
	message.Set(field, protoreflect.ValueOfBytes(references.Get(0).Message().Get(
		references.Get(0).Message().Descriptor().Fields().ByName("claim_hash"),
	).Bytes()))
	return nil
}

func CreateRepostTransaction(
	ctx context.Context, name, repostedClaimID string, amount uint64, address string,
	funding []*Account, fields map[string]any, channel *TransactionOutput,
) (*Transaction, error) {
	claim, err := BuildRepostClaim(repostedClaimID, fields)
	if err != nil {
		return nil, err
	}
	return createStreamClaimTransaction(ctx, nil, name, "", claim, amount, address, funding, channel)
}
