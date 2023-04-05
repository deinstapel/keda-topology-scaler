//go:generate protoc externalscaler.proto --go_out=externalscaler --go-grpc_out=externalscaler

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	pb "github.com/deinstapel/keda-topology-scaler/externalscaler"

	"github.com/samber/lo"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	v1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

type TopologyScaler struct {
	pb.ExternalScalerServer
	nodeInformer v1.NodeInformer
	informer     cache.SharedIndexInformer
}

const metaTopologyKey = "topologyKey"
const metaAmount = "targetInstances"
const metricName = "instancesPerTopologyKey"

func (t *TopologyScaler) parseMetadata(meta map[string]string) (string, int64, error) {
	topoKey := meta[metaTopologyKey]
	amountStr := meta[metaAmount]
	if topoKey == "" {
		return "", 0, errors.New("no topology key specified")
	}

	amount, err := strconv.ParseInt(amountStr, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("instances parsing error: %v", err)
	}

	return topoKey, amount, nil
}

func (t *TopologyScaler) loadTopologyValues(topoKey string, empty bool) ([]string, error) {
	nodes, err := t.nodeInformer.Lister().List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("failed to get nodes: %v", err)
	}
	uniqueValues := lo.Uniq(lo.Map(nodes, func(node *corev1.Node, _ int) string {
		if node.Annotations == nil {
			return ""
		}
		return node.Annotations[topoKey]
	}))
	if !empty {
		uniqueValues = lo.Filter(uniqueValues, func(s string, _ int) bool { return s != "" })
	}
	return uniqueValues, nil
}

func (t *TopologyScaler) IsActive(ctx context.Context, scaledObj *pb.ScaledObjectRef) (*pb.IsActiveResponse, error) {
	topo, _, err := t.parseMetadata(scaledObj.ScalerMetadata)
	if err != nil {
		return nil, fmt.Errorf("parsing failed: %v", err)
	}

	topoValues, err := t.loadTopologyValues(topo, true)
	if err != nil {
		return nil, err
	}
	return &pb.IsActiveResponse{
		Result: len(topoValues) > 0,
	}, nil
}

func (t *TopologyScaler) StreamIsActive(scaledObj *pb.ScaledObjectRef, epsServer pb.ExternalScaler_StreamIsActiveServer) error {
	topo, _, err := t.parseMetadata(scaledObj.ScalerMetadata)
	if err != nil {
		return fmt.Errorf("parsing failed: %v", err)
	}

	updateChan := make(chan struct{})

	reg, err := t.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(any, any) {
			updateChan <- struct{}{}
		},
		AddFunc: func(any) {
			updateChan <- struct{}{}
		},
		DeleteFunc: func(any) {
			updateChan <- struct{}{}
		},
	})
	if err != nil {
		return fmt.Errorf("failed setting up event watch: %v", err)
	}
	defer t.informer.RemoveEventHandler(reg)
outer:
	for {
		select {
		case <-epsServer.Context().Done():
			close(updateChan)
			break outer
		case <-updateChan:
			topoValues, err := t.loadTopologyValues(topo, true)
			if err != nil {
				return err
			}

			if err := epsServer.Send(&pb.IsActiveResponse{
				Result: len(topoValues) > 0,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *TopologyScaler) GetMetricSpec(ctx context.Context, scaledObj *pb.ScaledObjectRef) (*pb.GetMetricSpecResponse, error) {
	_, amount, err := t.parseMetadata(scaledObj.ScalerMetadata)
	if err != nil {
		return nil, fmt.Errorf("parse failed: %v", err)
	}
	return &pb.GetMetricSpecResponse{
		MetricSpecs: []*pb.MetricSpec{
			{
				MetricName: metricName,
				TargetSize: amount,
			},
		},
	}, nil
}

func (t *TopologyScaler) GetMetrics(ctx context.Context, metricsRequest *pb.GetMetricsRequest) (*pb.GetMetricsResponse, error) {
	topo, _, err := t.parseMetadata(metricsRequest.ScaledObjectRef.ScalerMetadata)
	if err != nil {
		return nil, fmt.Errorf("parse failed: %v", err)
	}
	topoKeys, err := t.loadTopologyValues(topo, true)
	if err != nil {
		return nil, err
	}
	return &pb.GetMetricsResponse{
		MetricValues: []*pb.MetricValue{
			{
				MetricName:  metricName,
				MetricValue: int64(len(topoKeys)),
			},
		},
	}, nil
}

func (t *TopologyScaler) WatchNodes(ctx context.Context, client kubernetes.Interface) {
	factory := informers.NewSharedInformerFactory(client, 0)
	t.nodeInformer = factory.Core().V1().Nodes()
	t.informer = t.nodeInformer.Informer()

	go factory.Start(ctx.Done())
	cacheTimeout, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if !cache.WaitForCacheSync(cacheTimeout.Done(), t.informer.HasSynced) {
		log.Fatalf("Timed out waiting for caches to sync\n")
	}
	log.Printf("node caches synced!")
}

func initK8sConfig() *rest.Config {
	if kc, ok := os.LookupEnv("KUBECONFIG"); ok {
		k8sConfig, err := clientcmd.BuildConfigFromFlags("", kc)
		if err != nil {
			log.Fatalf("KUBECONFIG was set, but parsing yielded error: %v\n", err)
		}
		return k8sConfig
	}
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("KUBECONFIG was not set and inClusterConfig yielded error: %v\n", err)
	}
	return k8sConfig
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	defer runtime.HandleCrash()

	k8sClient, err := kubernetes.NewForConfig(initK8sConfig())
	if err != nil {
		log.Fatalf("Failed to initialize k8s client: %v", err)
	}

	grpcServer := grpc.NewServer()
	lis, err := net.Listen("tcp", ":6000")
	if err != nil {
		log.Fatalf("Listen failed: %v\n", err)
	}

	scaler := &TopologyScaler{}
	scaler.WatchNodes(ctx, k8sClient)
	pb.RegisterExternalScalerServer(grpcServer, scaler)

	go func() {

		<-ctx.Done()
		fmt.Printf("Terminating\n")
		grpcServer.GracefulStop()
	}()

	fmt.Println("Listening on :6000")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
