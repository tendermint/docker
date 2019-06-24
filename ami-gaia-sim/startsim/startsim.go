package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
)

const (
	numSeeds = 420
	// If the number of jobs is < the number of seeds, simulation will crash
	numJobs                   = numSeeds
	instanceShutdownBehaviour = "terminate"
)

var (
	channelID   string
	slackToken  string
	numBlocks   string
	simPeriod   string
	gitRevision string
	notifyOnly  bool
)

func makeRanges() map[int]string {
	machines := make(map[int]string)
	var str strings.Builder
	index := 0
	for i := 0; i <= numSeeds; i++ {
		if i != 0 && math.Mod(float64(i), 35) == 0 {
			machines[index] = strings.TrimRight(str.String(), ",")
			str.Reset()
			index++
		}
		str.WriteString(strconv.Itoa(i) + ",")
	}

	if str.String() != "" {
		machines[index] = strings.TrimRight(str.String(), ",")
	}
	return machines
}

func getAmiId(gitRevision string, svc *ec2.EC2) (string, error) {
	input := &ec2.DescribeImagesInput{
		Filters: []*ec2.Filter{
			{
				Name: aws.String("name"),
				Values: []*string{
					aws.String("gaia-sim-" + gitRevision),
				},
			},
		},
	}

	result, err := svc.DescribeImages(input)
	if err != nil {
		return "", err
	}
	return *result.Images[0].ImageId, nil
}

func buildCommand(jobs int, seeds, token, channel, timeStamp, blocks, period string) string {
	return fmt.Sprintf("runsim -e -j %d -seeds \"%s\" -slack \"%s,%s,%s\" github.com/cosmos/cosmos-sdk/simapp %s %s TestFullAppSimulation;",
		jobs, seeds, token, channel, timeStamp, blocks, period)
}

func main() {
	flag.StringVar(&slackToken, "s", "", "Slack token")
	flag.StringVar(&channelID, "c", "", "Slack channel ID")
	flag.StringVar(&numBlocks, "b", "", "Number of blocks to simulate")
	flag.StringVar(&simPeriod, "p", "", "Simulation invariant check period")
	flag.StringVar(&gitRevision, "g", "", "The git revision on which the simulation is run")
	flag.BoolVar(&notifyOnly, "notify", false, "Send notification and exit")
	flag.Usage = func() {
		_, _ = fmt.Fprintf(flag.CommandLine.Output(),
			`Usage: %s [-s slacktoken] [-c channelID] [-b numblocks] [-p simperiod] [-g gitrevision]`, filepath.Base(os.Args[0]))
	}
	flag.Parse()

	if notifyOnly {
		_, err := slackMessage(slackToken, channelID, nil,
			fmt.Sprintf("Starting simulation AMI build. Git rev/hash/branch/tag: `%s`", gitRevision))
		if err != nil {
			log.Printf("ERROR: sending slack message: %v", err)
		}
		os.Exit(0)
	}

	msgTS, slackErr := slackMessage(slackToken, channelID, nil, "Spinning up simulation environments!")
	if slackErr != nil {
		log.Fatal("Could not report back to slack: " + slackErr.Error())
	}

	svc := ec2.New(session.Must(session.NewSession(&aws.Config{
		Region: aws.String("us-east-1"),
	})))

	amiId, err := getAmiId(gitRevision, svc)
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok {
			switch awsErr.Code() {
			default:
				log.Fatal(awsErr.Error())
			}
		} else {
			log.Fatal(err.Error())
		}
	}

	seeds := makeRanges()
	for rng := range seeds {
		log.Println(buildCommand(numJobs, seeds[rng], slackToken, channelID, msgTS, numBlocks, simPeriod))

		var userData strings.Builder
		userData.WriteString("#!/bin/bash \n")
		userData.WriteString("cd /home/ec2-user/go/src/github.com/cosmos/cosmos-sdk \n")
		userData.WriteString("source /etc/profile.d/set_env.sh \n")
		userData.WriteString(buildCommand(numJobs, seeds[rng], slackToken, channelID, msgTS, numBlocks, simPeriod))
		userData.WriteString("shutdown -h now")

		config := &ec2.RunInstancesInput{
			InstanceInitiatedShutdownBehavior: aws.String(instanceShutdownBehaviour),
			InstanceType:                      aws.String("c4.8xlarge"),
			ImageId:                           aws.String(amiId),
			KeyName:                           aws.String("wallet-nodes"),
			MaxCount:                          aws.Int64(1),
			MinCount:                          aws.Int64(1),
			UserData:                          aws.String(base64.StdEncoding.EncodeToString([]byte(userData.String()))),
		}
		result, err := svc.RunInstances(config)
		if err != nil {
			log.Fatal(err.Error())
		}

		for i := range result.Instances {
			log.Println(*result.Instances[i].InstanceId)
		}
	}
}
