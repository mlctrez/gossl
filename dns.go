package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/mlctrez/servicego"
)

const (
	EnvAWSAccessKeyID     = "AWS_ACCESS_KEY_ID"
	EnvAWSSecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	EnvAWSRegion          = "AWS_REGION"
	EnvRoute53HostedZone  = "ROUTE53_HOSTED_ZONE_ID"
	EnvRoute53CNAMETarget = "ROUTE53_CNAME_TARGET"
)

// DNSRecord represents the result of a DNS record lookup.
type DNSRecord struct {
	Exists bool
	Type   string
	Target string
}

// DNSClient is the interface for DNS operations used by API and Web handlers.
type DNSClient interface {
	LookupRecord(ctx context.Context, hostname string) (*DNSRecord, error)
	CreateCNAME(ctx context.Context, hostname string) error
	RemoveCNAME(ctx context.Context, hostname string) error
	CNAMETarget() string
}

// Route53Client handles optional Route53 DNS integration.
type Route53Client struct {
	client       *route53.Client
	hostedZoneID string
	cnameTarget  string
	logger       servicego.LoggerContainer
}

// CNAMETarget returns the configured CNAME target value.
func (c *Route53Client) CNAMETarget() string {
	return c.cnameTarget
}

// NewRoute53Client creates a Route53Client from environment variables.
// Returns nil if Route53 is not configured (missing env vars) and logs a warning.
func NewRoute53Client(logger servicego.LoggerContainer) *Route53Client {
	hostedZoneID := os.Getenv(EnvRoute53HostedZone)
	cnameTarget := os.Getenv(EnvRoute53CNAMETarget)
	accessKey := os.Getenv(EnvAWSAccessKeyID)
	secretKey := os.Getenv(EnvAWSSecretAccessKey)
	region := os.Getenv(EnvAWSRegion)

	if hostedZoneID == "" || cnameTarget == "" || accessKey == "" || secretKey == "" || region == "" {
		logger.Log().Warningf("Route53 DNS management disabled: missing one or more env vars (%s, %s, %s, %s, %s)",
			EnvAWSAccessKeyID, EnvAWSSecretAccessKey, EnvAWSRegion, EnvRoute53HostedZone, EnvRoute53CNAMETarget)
		return nil
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		logger.Log().Warningf("Route53 DNS management disabled: failed to load AWS config: %v", err)
		return nil
	}

	return &Route53Client{
		client:       route53.NewFromConfig(cfg),
		hostedZoneID: hostedZoneID,
		cnameTarget:  cnameTarget,
		logger:       logger,
	}
}

// fqdn ensures a hostname ends with a trailing dot for Route53 API compatibility.
func fqdn(hostname string) string {
	if strings.HasSuffix(hostname, ".") {
		return hostname
	}
	return hostname + "."
}

// LookupRecord checks if a DNS record exists for the hostname in the hosted zone.
func (c *Route53Client) LookupRecord(ctx context.Context, hostname string) (*DNSRecord, error) {
	name := fqdn(hostname)

	out, err := c.client.ListResourceRecordSets(ctx, &route53.ListResourceRecordSetsInput{
		HostedZoneId:    aws.String(c.hostedZoneID),
		StartRecordName: aws.String(name),
		MaxItems:        aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("Route53 ListResourceRecordSets: %w", err)
	}

	for _, rrs := range out.ResourceRecordSets {
		if !strings.EqualFold(aws.ToString(rrs.Name), name) {
			continue
		}
		rec := &DNSRecord{
			Exists: true,
			Type:   string(rrs.Type),
		}
		if len(rrs.ResourceRecords) > 0 {
			rec.Target = strings.TrimSuffix(aws.ToString(rrs.ResourceRecords[0].Value), ".")
		}
		return rec, nil
	}

	return &DNSRecord{Exists: false}, nil
}

// CreateCNAME creates a CNAME record pointing hostname to the configured cnameTarget.
// Uses UPSERT to handle both creation and update cases.
func (c *Route53Client) CreateCNAME(ctx context.Context, hostname string) error {
	name := fqdn(hostname)
	target := fqdn(c.cnameTarget)

	_, err := c.client.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(c.hostedZoneID),
		ChangeBatch: &types.ChangeBatch{
			Comment: aws.String("gossl: create CNAME for " + hostname),
			Changes: []types.Change{
				{
					Action: types.ChangeActionUpsert,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name: aws.String(name),
						Type: types.RRTypeCname,
						TTL:  aws.Int64(300),
						ResourceRecords: []types.ResourceRecord{
							{Value: aws.String(target)},
						},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("Route53 CreateCNAME for %s: %w", hostname, err)
	}
	return nil
}

// RemoveCNAME removes a CNAME record for hostname only if it points to the configured cnameTarget.
// If the record points to a different target, it is left unchanged and a warning is logged.
func (c *Route53Client) RemoveCNAME(ctx context.Context, hostname string) error {
	rec, err := c.LookupRecord(ctx, hostname)
	if err != nil {
		return err
	}

	if !rec.Exists {
		return nil
	}

	if rec.Type != string(types.RRTypeCname) {
		c.logger.Log().Warningf("Route53: record for %s is type %s, not CNAME; leaving unchanged", hostname, rec.Type)
		return nil
	}

	if !strings.EqualFold(rec.Target, strings.TrimSuffix(c.cnameTarget, ".")) {
		c.logger.Log().Warningf("Route53: CNAME for %s points to %s, not %s; leaving unchanged", hostname, rec.Target, c.cnameTarget)
		return nil
	}

	name := fqdn(hostname)
	target := fqdn(c.cnameTarget)

	_, err = c.client.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(c.hostedZoneID),
		ChangeBatch: &types.ChangeBatch{
			Comment: aws.String("gossl: remove CNAME for " + hostname),
			Changes: []types.Change{
				{
					Action: types.ChangeActionDelete,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name: aws.String(name),
						Type: types.RRTypeCname,
						TTL:  aws.Int64(300),
						ResourceRecords: []types.ResourceRecord{
							{Value: aws.String(target)},
						},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("Route53 RemoveCNAME for %s: %w", hostname, err)
	}
	return nil
}
