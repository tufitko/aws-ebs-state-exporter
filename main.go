package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/VictoriaMetrics/metrics"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	listenAddr     = flag.String("listen-addr", ":8080", "")
	requestTimeout = flag.Duration("request-timeout", time.Minute, "")
)

func main() {
	awsConfig := must(config.LoadDefaultConfig(context.Background()))

	handler := http.NewServeMux()
	handler.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		ctx, reqCancel := context.WithCancel(r.Context())
		defer reqCancel()
		awsGlobal := ec2.NewFromConfig(awsConfig)

		metricsSet := metrics.NewSet()
		regionsOutput, err := awsGlobal.DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var regions []string
		for _, reg := range regionsOutput.Regions {
			if reg.RegionName == nil {
				continue
			}
			regions = append(regions, *reg.RegionName)
		}

		var wg sync.WaitGroup
		wg.Add(len(regions))
		for _, region := range regions {
			go func(region string) {
				defer wg.Done()
				aws := ec2.NewFromConfig(awsConfig, func(options *ec2.Options) {
					options.Region = region
				})

				volumes, err := aws.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{})
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}

				for _, volume := range volumes.Volumes {
					var labels []string
					for _, tag := range volume.Tags {
						if tag.Key == nil || tag.Value == nil {
							continue
						}
						labels = append(labels, fmt.Sprintf("tag_%s=\"%s\"", formatLabelName(*tag.Key), *tag.Value))
					}
					labels = append(labels, fmt.Sprintf("state=\"%s\"", string(volume.State)))
					labels = append(labels, fmt.Sprintf("name=\"%s\"", *volume.VolumeId))
					labels = append(labels, fmt.Sprintf("type=\"%s\"", string(volume.VolumeType)))
					labels = append(labels, fmt.Sprintf("region=\"%s\"", region))
					metricsSet.GetOrCreateCounter(fmt.Sprintf("aws_ebs_volume_state{%s}", strings.Join(labels, ","))).Add(1)
				}
			}(region)
		}

		finish := make(chan struct{}, 1)

		go func() {
			wg.Wait()
			finish <- struct{}{}
		}()

		select {
		case <-time.After(*requestTimeout):
			http.Error(w, "timeout", http.StatusRequestTimeout)
			return
		case <-finish:
		}

		metricsSet.WritePrometheus(w)
	})

	srv := &http.Server{
		Addr:    *listenAddr,
		Handler: handler,
	}
	err := srv.ListenAndServe()
	if err != nil {
		log.Fatalf("listening err: %s", err)
	}
}

func must[T any](x T, err error) T {
	if err != nil {
		log.Fatalf("err: %s", err)
	}
	return x
}

var forbiddenLabelChars = regexp.MustCompile("[^a-zA-Z0-9:_]+")

func formatLabelName(text string) string {
	return forbiddenLabelChars.ReplaceAllString(text, "_")
}
