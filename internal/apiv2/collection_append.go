package apiv2

import "context"

type personalCollectionAppender interface {
	AppendPersonalCollectionItem(context.Context, int, string, string, string) error
}

type adminCollectionAppender interface {
	AppendAdminCollectionItem(context.Context, string, string) error
}

func supportsPersonalCollectionAppend(svc any) bool {
	_, ok := svc.(personalCollectionAppender)
	return ok
}

func (reg *Registry) appendPersonalCollectionItem(ctx context.Context, in *PersonalCollectionAddItemInput) (*struct{}, error) {
	svc, ok := reg.deps.PersonalCollections.(personalCollectionAppender)
	if !ok {
		return nil, NewProblem(TypeCapabilityUnsupported, "Atomic collection append is unavailable.")
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	if err := svc.AppendPersonalCollectionItem(ctx, userID, profileFrom(ctx), string(in.ID), string(in.ItemID)); err != nil {
		return nil, collectionProblem(err)
	}
	return nil, nil
}

func (reg *Registry) appendAdminCollectionItem(ctx context.Context, in *AdminCollectionMemberInput) (*struct{}, error) {
	svc, ok := reg.deps.AdminCollections.(adminCollectionAppender)
	if !ok {
		return nil, NewProblem(TypeCapabilityUnsupported, "Atomic collection append is unavailable.")
	}
	if err := svc.AppendAdminCollectionItem(ctx, string(in.ID), string(in.ItemID)); err != nil {
		return nil, adminCollectionError(err)
	}
	return nil, nil
}
