/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package k8sapi

import (
	"crypto/x509"
	"fmt"
	"github.com/golang/glog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/kops/upup/pkg/fi"
	"math/big"
	"time"
)

type KubernetesKeystore struct {
	client    kubernetes.Interface
	namespace string

	//mutex     sync.Mutex
	//cacheCaCertificates *certificates
	//cacheCaPrivateKeys  *privateKeys
}

var _ fi.Keystore = &KubernetesKeystore{}

func NewKubernetesKeystore(client kubernetes.Interface, namespace string) fi.Keystore {
	c := &KubernetesKeystore{
		client:    client,
		namespace: namespace,
	}

	return c
}

func (c *KubernetesKeystore) issueCert(id string, serial *big.Int, privateKey *fi.PrivateKey, template *x509.Certificate) (*fi.Certificate, error) {
	glog.Infof("Issuing new certificate: %q", id)

	template.SerialNumber = serial

	caCert, caKey, err := c.FindKeypair(fi.CertificateId_CA)
	if err != nil {
		return nil, err
	}

	if caCert == nil || caCert.Certificate == nil || caKey == nil || caKey.Key == nil {
		return nil, fmt.Errorf("CA keypair was not found; cannot issue certificates")
	}

	cert, err := fi.SignNewCertificate(privateKey, template, caCert.Certificate, caKey)
	if err != nil {
		return nil, err
	}

	err = c.StoreKeypair(id, cert, privateKey)
	if err != nil {
		return nil, err
	}

	return cert, nil
}

func (c *KubernetesKeystore) findSecret(id string) (*v1.Secret, error) {
	secret, err := c.client.CoreV1().Secrets(c.namespace).Get(id, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("error reading secret %s/%s from kubernetes: %v", c.namespace, id, err)
	}
	return secret, nil
}

func (c *KubernetesKeystore) FindKeypair(id string) (*fi.Certificate, *fi.PrivateKey, error) {
	secret, err := c.findSecret(id)
	if err != nil {
		return nil, nil, err
	}

	if secret == nil {
		return nil, nil, nil
	}

	keypair, err := ParseKeypairSecret(secret)
	if err != nil {
		return nil, nil, fmt.Errorf("error parsing secret %s/%s from kubernetes: %v", c.namespace, id, err)
	}

	return keypair.Certificate, keypair.PrivateKey, nil
}

func (c *KubernetesKeystore) CreateKeypair(id string, template *x509.Certificate, privateKey *fi.PrivateKey) (*fi.Certificate, error) {
	t := time.Now().UnixNano()
	serial := fi.BuildPKISerial(t)

	cert, err := c.issueCert(id, serial, privateKey, template)
	if err != nil {
		return nil, err
	}

	return cert, nil
}

func (c *KubernetesKeystore) StoreKeypair(id string, cert *fi.Certificate, privateKey *fi.PrivateKey) error {
	keypair := &KeypairSecret{
		Namespace:   c.namespace,
		Name:        id,
		Certificate: cert,
		PrivateKey:  privateKey,
	}

	secret, err := keypair.Encode()
	if err != nil {
		return fmt.Errorf("error encoding keypair: %+v  err: %s", keypair, err)
	}
	createdSecret, err := c.client.CoreV1().Secrets(c.namespace).Create(secret)
	if err != nil {
		return fmt.Errorf("error creating secret %s/%s: %v", secret.Namespace, secret.Name, err)
	}

	created, err := ParseKeypairSecret(createdSecret)
	if err != nil {
		return fmt.Errorf("created secret did not round-trip (%s/%s): %v", c.namespace, id, err)
	}
	if created == nil || created.Certificate == nil || created.PrivateKey == nil {
		return fmt.Errorf("created secret did not round-trip (%s/%s): could not read back", c.namespace, id)
	}

	return err
}
