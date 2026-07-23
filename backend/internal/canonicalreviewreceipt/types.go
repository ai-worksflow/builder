package canonicalreviewreceipt

const (
	ReceiptSchemaVersion       = "worksflow-canonical-review-approval-receipt/v1"
	ReviewRequestSchemaVersion = "worksflow-canonical-review-request-snapshot/v1"
	RevisionSchemaVersion      = "worksflow-canonical-review-revision-snapshot/v1"
	PolicySchemaVersion        = "worksflow-canonical-review-policy-snapshot/v1"
	DecisionsSchemaVersion     = "worksflow-canonical-review-decisions-snapshot/v1"
	GovernanceSchemaVersion    = "worksflow-canonical-review-governance-snapshot/v1"
	ApprovalSchemaVersion      = "worksflow-canonical-review-approval-snapshot/v1"
	ReceiptMediaType           = "application/vnd.worksflow.canonical-review-approval-receipt+json;version=1"

	ReviewRequestHashDomain = "worksflow.canonical-review.review-request/v1"
	RevisionHashDomain      = "worksflow.canonical-review.revision/v1"
	PolicyHashDomain        = "worksflow.canonical-review.policy/v1"
	DecisionsHashDomain     = "worksflow.canonical-review.decisions/v1"
	GovernanceHashDomain    = "worksflow.canonical-review.governance/v1"
	ApprovalHashDomain      = "worksflow.canonical-review.approval/v1"
	ReceiptHashDomain       = "worksflow.canonical-review.receipt/v1"
)

type ReviewRequestSnapshot struct {
	ArtifactID             string `json:"artifactId"`
	ClosedAt               string `json:"closedAt"`
	ClosedByDecisionID     string `json:"closedByDecisionId"`
	ContentHash            string `json:"contentHash"`
	ID                     string `json:"id"`
	ProjectID              string `json:"projectId"`
	RequestedAt            string `json:"requestedAt"`
	RequestedBy            string `json:"requestedBy"`
	ReviewAuthorityVersion int    `json:"reviewAuthorityVersion"`
	RevisionID             string `json:"revisionId"`
	SchemaVersion          string `json:"schemaVersion"`
	Status                 string `json:"status"`
}

type RevisionSnapshot struct {
	ApprovedAt               string  `json:"approvedAt"`
	ArtifactID               string  `json:"artifactId"`
	ByteSize                 int64   `json:"byteSize"`
	ChangeSource             string  `json:"changeSource"`
	ChangeSummary            string  `json:"changeSummary"`
	ContentHash              string  `json:"contentHash"`
	ContentRef               string  `json:"contentRef"`
	ContentStore             string  `json:"contentStore"`
	CreatedAt                string  `json:"createdAt"`
	CreatedBy                string  `json:"createdBy"`
	ID                       string  `json:"id"`
	ImplementationProposalID *string `json:"implementationProposalId"`
	ParentRevisionID         *string `json:"parentRevisionId"`
	ProposalID               *string `json:"proposalId"`
	RevisionNumber           int64   `json:"revisionNumber"`
	SchemaVersion            string  `json:"schemaVersion"`
	SourceManifestID         *string `json:"sourceManifestId"`
	SupersededAt             *string `json:"supersededAt"`
	WorkflowStatus           string  `json:"workflowStatus"`
	ArtifactSchemaVersion    int     `json:"artifactSchemaVersion"`
}

type PolicyValue struct {
	GovernanceMode        string   `json:"governanceMode"`
	MinimumApprovals      int      `json:"minimumApprovals"`
	ProhibitSelfReview    bool     `json:"prohibitSelfReview"`
	ReviewerIDs           []string `json:"reviewerIds"`
	SoloSelfReviewOwnerID *string  `json:"soloSelfReviewOwnerId"`
}

type PolicySnapshot struct {
	SchemaVersion string      `json:"schemaVersion"`
	Value         PolicyValue `json:"value"`
}

type DecisionAuthorityFacts struct {
	ExplicitConfirmation bool    `json:"explicitConfirmation"`
	GovernanceMode       string  `json:"governanceMode"`
	OwnerCount           int     `json:"ownerCount"`
	PreconditionETag     string  `json:"preconditionETag"`
	ReviewerRole         string  `json:"reviewerRole"`
	SoleOwnerID          *string `json:"soleOwnerId"`
	Version              int     `json:"version"`
}

type DecisionSnapshot struct {
	AuthorityFacts DecisionAuthorityFacts `json:"authorityFacts"`
	CreatedAt      string                 `json:"createdAt"`
	Decision       string                 `json:"decision"`
	ID             string                 `json:"id"`
	ReviewerID     string                 `json:"reviewerId"`
	SoloSelfReview bool                   `json:"soloSelfReview"`
	Summary        string                 `json:"summary"`
}

type DecisionsSnapshot struct {
	Decisions     []DecisionSnapshot `json:"decisions"`
	SchemaVersion string             `json:"schemaVersion"`
}

type GovernanceSnapshot struct {
	Mode          string  `json:"mode"`
	OwnerCount    int     `json:"ownerCount"`
	SchemaVersion string  `json:"schemaVersion"`
	SoleOwnerID   *string `json:"soleOwnerId"`
}

type ApprovalSnapshot struct {
	ApprovalCount            int      `json:"approvalCount"`
	ApprovalDecisionIDs      []string `json:"approvalDecisionIds"`
	ApprovedAt               string   `json:"approvedAt"`
	ArtifactID               string   `json:"artifactId"`
	ArtifactKind             string   `json:"artifactKind"`
	ArtifactLatestApprovedID string   `json:"artifactLatestApprovedRevisionId"`
	ArtifactLatestRevisionID string   `json:"artifactLatestRevisionId"`
	ArtifactLifecycle        string   `json:"artifactLifecycle"`
	ArtifactVersion          int64    `json:"artifactVersion"`
	ClosedByDecisionID       string   `json:"closedByDecisionId"`
	MinimumApprovals         int      `json:"minimumApprovals"`
	ProjectID                string   `json:"projectId"`
	RevisionContentHash      string   `json:"revisionContentHash"`
	RevisionID               string   `json:"revisionId"`
	SchemaVersion            string   `json:"schemaVersion"`
	SoloSelfReview           bool     `json:"soloSelfReview"`
	SubjectAuthorID          string   `json:"subjectAuthorId"`
}

type ComponentDigests struct {
	Approval      string `json:"approval"`
	Decisions     string `json:"decisions"`
	Governance    string `json:"governance"`
	Policy        string `json:"policy"`
	ReviewRequest string `json:"reviewRequest"`
	Revision      string `json:"revision"`
}

type Receipt struct {
	Approval         ApprovalSnapshot      `json:"approval"`
	ComponentDigests ComponentDigests      `json:"componentDigests"`
	Decisions        DecisionsSnapshot     `json:"decisions"`
	Governance       GovernanceSnapshot    `json:"governance"`
	IssuedAt         string                `json:"issuedAt"`
	MediaType        string                `json:"mediaType"`
	Policy           PolicySnapshot        `json:"policy"`
	ReviewRequest    ReviewRequestSnapshot `json:"reviewRequest"`
	Revision         RevisionSnapshot      `json:"revision"`
	SchemaVersion    string                `json:"schemaVersion"`
}

type Compiled struct {
	Receipt Receipt
	Bytes   []byte
	Hash    string
	Parts   map[string][]byte
}
