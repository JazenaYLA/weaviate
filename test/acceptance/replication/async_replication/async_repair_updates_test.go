//                           _       _
// __      _____  __ ___   ___  __ _| |_ ___
// \ \ /\ / / _ \/ _` \ \ / / |/ _` | __/ _ \
//  \ V  V /  __/ (_| |\ V /| | (_| | ||  __/
//   \_/\_/ \___|\__,_| \_/ |_|\__,_|\__\___|
//
//  Copyright © 2016 - 2024 Weaviate B.V. All rights reserved.
//
//  CONTACT: hello@weaviate.io
//

package replication

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/entities/models"
	"github.com/weaviate/weaviate/test/acceptance/replication/common"
	"github.com/weaviate/weaviate/test/docker"
	"github.com/weaviate/weaviate/test/helper"
	"github.com/weaviate/weaviate/test/helper/sample-schema/articles"
	"github.com/weaviate/weaviate/usecases/replica"
)

func (suite *AsyncReplicationTestSuite) TestAsyncRepairObjectUpdateScenario() {
	t := suite.T()
	mainCtx := context.Background()

	clusterSize := 3

	compose, err := docker.New().
		WithWeaviateCluster(clusterSize).
		WithText2VecContextionary().
		Start(mainCtx)
	require.Nil(t, err)
	defer func() {
		if err := compose.Terminate(mainCtx); err != nil {
			t.Fatalf("failed to terminate test containers: %s", err.Error())
		}
	}()

	ctx, cancel := context.WithTimeout(mainCtx, 10*time.Minute)
	defer cancel()

	paragraphClass := articles.ParagraphsClass()

	t.Run("create schema", func(t *testing.T) {
		paragraphClass.ReplicationConfig = &models.ReplicationConfig{
			Factor:       int64(clusterSize),
			AsyncEnabled: true,
		}
		paragraphClass.Vectorizer = "text2vec-contextionary"

		helper.SetupClient(compose.GetWeaviate().URI())
		helper.CreateClass(t, paragraphClass)
	})

	node := 2

	t.Run(fmt.Sprintf("stop node %d", node), func(t *testing.T) {
		common.StopNodeAt(ctx, t, compose, node)
	})

	t.Run("upsert paragraphs", func(t *testing.T) {
		batch := make([]*models.Object, len(paragraphIDs))
		for i, id := range paragraphIDs {
			batch[i] = articles.NewParagraph().
				WithID(id).
				WithContents(fmt.Sprintf("paragraph#%d", i)).
				Object()
		}

		// choose one more node to insert the objects into
		var targetNode int
		for {
			targetNode = 1 + rand.Intn(clusterSize)
			if targetNode != node {
				break
			}
		}

		common.CreateObjectsCL(t, compose.GetWeaviateNode(targetNode).URI(), batch, replica.One)
	})

	t.Run(fmt.Sprintf("restart node %d", node), func(t *testing.T) {
		common.StartNodeAt(ctx, t, compose, node)
		time.Sleep(5 * time.Second)
	})

	t.Run(fmt.Sprintf("assert node %d has all the objects at its latest version", node), func(t *testing.T) {
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			count := common.CountObjects(t, compose.GetWeaviateNode(node).URI(), paragraphClass.Class)
			assert.EqualValues(ct, len(paragraphIDs), count)

			for i, id := range paragraphIDs {
				resp, err := common.GetObjectCL(t, compose.GetWeaviateNode(node).URI(), paragraphClass.Class, id, replica.One)
				assert.NoError(ct, err)
				assert.NotNil(ct, resp)

				if resp == nil {
					continue
				}

				assert.Equal(ct, id, resp.ID)

				props := resp.Properties.(map[string]interface{})
				props["contents"] = fmt.Sprintf("paragraph#%d", i)
			}
		}, 60*time.Second, 1*time.Second, "not all the objects have been asynchronously replicated")
	})
}
