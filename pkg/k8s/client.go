package k8s

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func Clientset(kubecfgpath string) (kubernetes.Interface, error) {
	kubecfg, err := config(kubecfgpath)
	if err != nil {
		return nil, err
	}

	return kubernetes.NewForConfig(kubecfg)
}

func config(kubecfgpath string) (*rest.Config, error) {
	if kubecfgpath == "" {
		return rest.InClusterConfig()
	}

	return clientcmd.BuildConfigFromFlags("", kubecfgpath)
}
