package ops

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"

	"github.com/nu7hatch/gouuid"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/external"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
)

type ClusterConfiguration struct {
	Name             string
	AppInstanceType  string
	AppInstanceCount int
	DBInstanceType   string
}

func generateSSHKey() (privateKeyPEM, authorizedKey []byte, err error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	privateKeyDER := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyDER,
	})

	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	authorizedKey = ssh.MarshalAuthorizedKey(publicKey)
	return
}

func CreateCluster(cluster *ClusterConfiguration) error {
	cfg, err := external.LoadDefaultAWSConfig()
	if err != nil {
		return errors.Wrap(err, "unable to load AWS config")
	}

	requestUUID, err := uuid.NewV4()
	if err != nil {
		return errors.Wrap(err, "unable to generate request UUID")
	}
	requestToken := "mattermost-loadtest-ops-" + requestUUID.String()

	dbPasswordBytes := make([]byte, 15)
	if _, err = rand.Read(dbPasswordBytes); err != nil {
		return errors.Wrap(err, "unable to generate database password")
	}
	dbPassword := base64.RawURLEncoding.EncodeToString(dbPasswordBytes)

	sshPrivateKeyPEM, sshAuthorizedKey, err := generateSSHKey()
	if err != nil {
		return errors.Wrap(err, "unable to generate ssh key")
	}

	cf := cloudformation.New(cfg)
	req := cf.CreateStackRequest(&cloudformation.CreateStackInput{
		ClientRequestToken: aws.String(requestToken),
		StackName:          aws.String(cluster.Name),
		TemplateBody:       aws.String(clusterCloudFormationTemplate),
		Parameters: []cloudformation.Parameter{
			{
				ParameterKey:   aws.String("AppInstanceCount"),
				ParameterValue: aws.String(fmt.Sprintf("%d", cluster.AppInstanceCount)),
			},
			{
				ParameterKey:   aws.String("AppInstanceType"),
				ParameterValue: aws.String(cluster.AppInstanceType),
			},
			{
				ParameterKey:   aws.String("DBInstanceType"),
				ParameterValue: aws.String(cluster.DBInstanceType),
			},
			{
				ParameterKey:   aws.String("DBPassword"),
				ParameterValue: aws.String(dbPassword),
			},
			{
				ParameterKey:   aws.String("SSHAuthorizedKey"),
				ParameterValue: aws.String(string(sshAuthorizedKey)),
			},
		},
	})

	resp, err := req.Send()
	if err != nil {
		return errors.Wrap(err, "unable to create stack")
	}

	logrus.Info("creating cluster...")

	if err := SaveClusterInfo(cluster.Name, &ClusterInfo{
		CloudFormationStackId: *resp.StackId,
		DatabasePassword:      dbPassword,
		SSHKey:                sshPrivateKeyPEM,
	}); err != nil {
		return errors.Wrap(err, "unable to save cluster info. please delete the CloudFormation stack manually")
	}

	if status, err := monitorCloudFormationStack(cf, *resp.StackId, requestToken); err != nil || status != cloudformation.StackStatusCreateComplete {
		return errors.Wrap(err, "stack creation failed. you may need to delete the CloudFormation stack manually and try again")
	}

	return nil
}
