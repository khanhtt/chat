// +build dynamodb

package dynamodb

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/khanhtt/chat/server/store"
	t "github.com/khanhtt/chat/server/store/types"
)

type DynamoDBAdapter struct {
	svc *dynamodb.DynamoDB
}

type UserKey struct {
	Id string
}

type AuthKey struct {
	Unique string `json:"unique"`
}

type TagUniqueKey struct {
	Id string
}

type TopicKey struct {
	Id string
}

type SubscriptionKey struct {
	Id string
}

type MessageKey struct {
	Topic string
	SeqId int
}

var (
	USERS_TABLE            string = "HxroDevUsers"
	AUTH_TABLE             string = "HxroDevAuth"
	TAGUNIQUE_TABLE        string = "HxroDevTagUnique"
	TOPICS_TABLE           string = "HxroDevTopics"
	SUBSCRIPTIONS_TABLE    string = "HxroDevSubscriptions"
	MESSAGES_TABLE         string = "HxroDevMessages"
	MAX_RESULTS            int    = 100
	MAX_DELETE_ITEMS       int    = 25
	MAX_MESSAGES_RETRIEVED int    = 100 // max messages retrieved in single get messages operation

	EXPIRE_DURATION_MESSAGE_GROUP int = 604800   // 1 week
	EXPIRE_DURATION_MESSAGE_ME    int = 2592000  // 1 month
	EXPIRE_DURATION_MESSAGE_P2P   int = 31536000 // 1 year

	SELF_TALK_SERVICE_USER_ID t.Uid = 5
	DEBUG_MODE                bool
)

const (
	MAX_BATCH_GET_ITEM   int = 100
	MAX_FIND_SUBS_RESULT int = 100
	MAX_DEVICES_PER_USER int = 100
	MAX_USERS_TO_FETCH   int = 100
)

type ErrorLogger struct {
	Tag string
}

func (e *ErrorLogger) LogError(err error) {
	log.Printf("[%v] %v\n", e.Tag, err)
}

func logDebugMessage(msg string) {
	if DEBUG_MODE {
		log.Println("[RDebug] " + msg)
	}
}

type Settings struct {
	Region            string      `json:"region"`
	Endpoint          string      `json:"endpoint"`
	Profile           string      `json:"profile"`
	SelfChatServiceId uint64      `json:"self_chat_service_id"`
	TableConfig       TableConfig `json:"table_config"`
	IndexConfig       IndexConfig `json:"index_config"`
	DebugMode         bool        `json:"debug_mode"`
}

type ProvisionedThroughputSettings struct {
	ReadCapacity  int64 `json:"read_capacity"`
	WriteCapacity int64 `json:"write_capacity"`
}

type TableDetailSettings struct {
	Name                  string                        `json:"name"`
	ProvisionedThroughput ProvisionedThroughputSettings `json:"provisioned_throughput"`
}

type TableConfig struct {
	Users         TableDetailSettings `json:"users"`
	Auth          TableDetailSettings `json:"auth"`
	TagUnique     TableDetailSettings `json:"tagunique"`
	Topics        TableDetailSettings `json:"topics"`
	Subscriptions TableDetailSettings `json:"subscriptions"`
	Messages      TableDetailSettings `json:"messages"`
}

type IndexDetailSettings struct {
	ProvisionedThroughput ProvisionedThroughputSettings `json:"provisioned_throughput"`
}

type IndexConfig struct {
	UserID        IndexDetailSettings `json:"userid"`
	Source        IndexDetailSettings
	UserUpdatedAt IndexDetailSettings
	Topic         IndexDetailSettings
}

// represent all settings from config file
var settings Settings

// function to get ean, eav, & ue from arbitrary update item input
func parseEanEavUeUpdateItem(update map[string]interface{}) (map[string]*string, map[string]*dynamodb.AttributeValue, *string, error) {

	// prepare ean, eav, ue
	_update := make(map[string]interface{})
	ean := make(map[string]*string)
	ue := "set "
	for k, v := range update {
		attributeNameLbl := "#" + k
		attributeValueLbl := ":" + k
		ean[attributeNameLbl] = aws.String(k)
		ue = ue + fmt.Sprintf("%v=%v, ", attributeNameLbl, attributeValueLbl)
		_update[attributeValueLbl] = v
	}
	ue = ue[:len(ue)-2]
	eav, err := dynamodbattribute.MarshalMap(_update)

	return ean, eav, aws.String(ue), err
}

func (a *DynamoDBAdapter) Open(jsonstring string) error {

	if a.IsOpen() {
		return errors.New("adapter dynamodb is already connected")
	}

	// parse settings from config file
	if err := json.Unmarshal([]byte(jsonstring), &settings); err != nil {
		return err
	}

	// initialize commonly used variables
	USERS_TABLE = settings.TableConfig.Users.Name
	AUTH_TABLE = settings.TableConfig.Auth.Name
	TAGUNIQUE_TABLE = settings.TableConfig.TagUnique.Name
	TOPICS_TABLE = settings.TableConfig.Topics.Name
	SUBSCRIPTIONS_TABLE = settings.TableConfig.Subscriptions.Name
	MESSAGES_TABLE = settings.TableConfig.Messages.Name
	SELF_TALK_SERVICE_USER_ID = t.Uid(settings.SelfChatServiceId)
	DEBUG_MODE = settings.DebugMode

	// initialize dynamodb connection
	sess, err := session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region:   aws.String(settings.Region),
			Endpoint: aws.String(settings.Endpoint),
		},
		Profile: settings.Profile,
	})
	if err != nil {
		return err
	}
	a.svc = dynamodb.New(sess)

	return nil
}

func (a *DynamoDBAdapter) Close() error {
	a.svc = nil
	return nil
}

func (a *DynamoDBAdapter) IsOpen() bool {
	return a.svc != nil
}

func (a *DynamoDBAdapter) CreateDb(reset bool) error {

	var err error

	if reset {
		// delete users table
		_, err = a.svc.DeleteTable(&dynamodb.DeleteTableInput{
			TableName: aws.String(USERS_TABLE),
		})
		if err != nil {
			if aerr, ok := err.(awserr.Error); (ok && aerr.Code() != dynamodb.ErrCodeResourceNotFoundException) || !ok {
				log.Println(err)
				return err
			}
		}

		// delete auth table
		_, err = a.svc.DeleteTable(&dynamodb.DeleteTableInput{
			TableName: aws.String(AUTH_TABLE),
		})
		if err != nil {
			if aerr, ok := err.(awserr.Error); (ok && aerr.Code() != dynamodb.ErrCodeResourceNotFoundException) || !ok {
				log.Println(err)
				return err
			}
		}

		// delete tagunique table
		_, err = a.svc.DeleteTable(&dynamodb.DeleteTableInput{
			TableName: aws.String(TAGUNIQUE_TABLE),
		})
		if err != nil {
			if aerr, ok := err.(awserr.Error); (ok && aerr.Code() != dynamodb.ErrCodeResourceNotFoundException) || !ok {
				log.Println(err)
				return err
			}
		}

		// delete topics table
		_, err = a.svc.DeleteTable(&dynamodb.DeleteTableInput{
			TableName: aws.String(TOPICS_TABLE),
		})
		if err != nil {
			if aerr, ok := err.(awserr.Error); (ok && aerr.Code() != dynamodb.ErrCodeResourceNotFoundException) || !ok {
				log.Println(err)
				return err
			}
		}

		// delete subscriptions table
		_, err = a.svc.DeleteTable(&dynamodb.DeleteTableInput{
			TableName: aws.String(SUBSCRIPTIONS_TABLE),
		})
		if err != nil {
			if aerr, ok := err.(awserr.Error); (ok && aerr.Code() != dynamodb.ErrCodeResourceNotFoundException) || !ok {
				log.Println(err)
				return err
			}
		}

		// delete messages table
		_, err = a.svc.DeleteTable(&dynamodb.DeleteTableInput{
			TableName: aws.String(MESSAGES_TABLE),
		})
		if err != nil {
			if aerr, ok := err.(awserr.Error); (ok && aerr.Code() != dynamodb.ErrCodeResourceNotFoundException) || !ok {
				log.Println(err)
				return err
			}
		}

		// wait until all tables deleted
		a.svc.WaitUntilTableNotExists(&dynamodb.DescribeTableInput{
			TableName: aws.String(USERS_TABLE),
		})
		a.svc.WaitUntilTableNotExists(&dynamodb.DescribeTableInput{
			TableName: aws.String(AUTH_TABLE),
		})
		a.svc.WaitUntilTableNotExists(&dynamodb.DescribeTableInput{
			TableName: aws.String(TAGUNIQUE_TABLE),
		})
		a.svc.WaitUntilTableNotExists(&dynamodb.DescribeTableInput{
			TableName: aws.String(TOPICS_TABLE),
		})
		a.svc.WaitUntilTableNotExists(&dynamodb.DescribeTableInput{
			TableName: aws.String(SUBSCRIPTIONS_TABLE),
		})
		a.svc.WaitUntilTableNotExists(&dynamodb.DescribeTableInput{
			TableName: aws.String(MESSAGES_TABLE),
		})
	}

	var input *dynamodb.CreateTableInput

	// create table with no secondary indexes
	log.Printf("Creating table with no secondary indexes: %v, %v, %v", USERS_TABLE, TOPICS_TABLE, MESSAGES_TABLE)

	// create users table
	input = &dynamodb.CreateTableInput{
		AttributeDefinitions: []*dynamodb.AttributeDefinition{
			{
				AttributeName: aws.String("Id"),
				AttributeType: aws.String("S"),
			},
		},
		KeySchema: []*dynamodb.KeySchemaElement{
			{
				AttributeName: aws.String("Id"),
				KeyType:       aws.String("HASH"),
			},
		},
		ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(settings.TableConfig.Users.ProvisionedThroughput.ReadCapacity),
			WriteCapacityUnits: aws.Int64(settings.TableConfig.Users.ProvisionedThroughput.WriteCapacity),
		},
		TableName: aws.String(USERS_TABLE),
	}
	_, err = a.svc.CreateTable(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() != dynamodb.ErrCodeResourceInUseException {
			log.Println(err)
			return err
		}
	}

	// create topics table
	input = &dynamodb.CreateTableInput{
		AttributeDefinitions: []*dynamodb.AttributeDefinition{
			{
				AttributeName: aws.String("Id"),
				AttributeType: aws.String("S"),
			},
		},
		KeySchema: []*dynamodb.KeySchemaElement{
			{
				AttributeName: aws.String("Id"),
				KeyType:       aws.String("HASH"),
			},
		},
		ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(settings.TableConfig.Topics.ProvisionedThroughput.ReadCapacity),
			WriteCapacityUnits: aws.Int64(settings.TableConfig.Topics.ProvisionedThroughput.WriteCapacity),
		},
		TableName: aws.String(TOPICS_TABLE),
	}
	_, err = a.svc.CreateTable(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() != dynamodb.ErrCodeResourceInUseException {
			log.Println(err)
			return err
		}
	}

	// create messages table
	input = &dynamodb.CreateTableInput{
		AttributeDefinitions: []*dynamodb.AttributeDefinition{
			{
				AttributeName: aws.String("Topic"),
				AttributeType: aws.String("S"),
			},
			{
				AttributeName: aws.String("SeqId"),
				AttributeType: aws.String("N"),
			},
		},
		KeySchema: []*dynamodb.KeySchemaElement{
			{
				AttributeName: aws.String("Topic"),
				KeyType:       aws.String("HASH"),
			},
			{
				AttributeName: aws.String("SeqId"),
				KeyType:       aws.String("RANGE"),
			},
		},
		ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(settings.TableConfig.Messages.ProvisionedThroughput.ReadCapacity),
			WriteCapacityUnits: aws.Int64(settings.TableConfig.Messages.ProvisionedThroughput.WriteCapacity),
		},
		TableName: aws.String(MESSAGES_TABLE),
	}
	_, err = a.svc.CreateTable(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() != dynamodb.ErrCodeResourceInUseException {
			log.Println(err)
			return err
		}
	}

	// wait for users, topics, & messages tables created
	a.svc.WaitUntilTableExists(&dynamodb.DescribeTableInput{
		TableName: aws.String(USERS_TABLE),
	})
	log.Printf("%v table created", USERS_TABLE)
	a.svc.WaitUntilTableExists(&dynamodb.DescribeTableInput{
		TableName: aws.String(TOPICS_TABLE),
	})
	log.Printf("%v table created", TOPICS_TABLE)
	a.svc.WaitUntilTableExists(&dynamodb.DescribeTableInput{
		TableName: aws.String(MESSAGES_TABLE),
	})
	log.Printf("%v table created", MESSAGES_TABLE)

	// only set TTL field if it is on online mode
	if len(settings.Profile) > 0 {
		// set TTL field to messages table
		_, err = a.svc.UpdateTimeToLive(&dynamodb.UpdateTimeToLiveInput{
			TableName: aws.String(MESSAGES_TABLE),
			TimeToLiveSpecification: &dynamodb.TimeToLiveSpecification{
				AttributeName: aws.String("ExpireTime"),
				Enabled:       aws.Bool(true),
			},
		})
		if err != nil && !strings.Contains(err.Error(), "TimeToLive is already enabled") {
			log.Println(err)
			return err
		}
		log.Printf("%v ttl field set to active", MESSAGES_TABLE)
	}

	// create table with secondary indexes
	log.Printf("Creating tables with secondary indexes: %v, %v, %v", AUTH_TABLE, TAGUNIQUE_TABLE, SUBSCRIPTIONS_TABLE)

	// create auth table
	input = &dynamodb.CreateTableInput{
		AttributeDefinitions: []*dynamodb.AttributeDefinition{
			{
				AttributeName: aws.String("unique"),
				AttributeType: aws.String("S"),
			},
			{
				AttributeName: aws.String("userid"),
				AttributeType: aws.String("S"),
			},
		},
		KeySchema: []*dynamodb.KeySchemaElement{
			{
				AttributeName: aws.String("unique"),
				KeyType:       aws.String("HASH"),
			},
		},
		ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(settings.TableConfig.Auth.ProvisionedThroughput.ReadCapacity),
			WriteCapacityUnits: aws.Int64(settings.TableConfig.Auth.ProvisionedThroughput.WriteCapacity),
		},
		GlobalSecondaryIndexes: []*dynamodb.GlobalSecondaryIndex{
			{
				IndexName: aws.String("userid"),
				KeySchema: []*dynamodb.KeySchemaElement{
					{
						AttributeName: aws.String("userid"),
						KeyType:       aws.String("HASH"),
					},
				},
				Projection: &dynamodb.Projection{
					ProjectionType: aws.String("ALL"),
				},
				ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
					ReadCapacityUnits:  aws.Int64(settings.IndexConfig.UserID.ProvisionedThroughput.ReadCapacity),
					WriteCapacityUnits: aws.Int64(settings.IndexConfig.UserID.ProvisionedThroughput.WriteCapacity),
				},
			},
		},
		TableName: aws.String(AUTH_TABLE),
	}
	_, err = a.svc.CreateTable(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() != dynamodb.ErrCodeResourceInUseException {
			log.Println(err)
			return err
		}
	}
	a.svc.WaitUntilTableExists(&dynamodb.DescribeTableInput{
		TableName: aws.String(AUTH_TABLE),
	})
	log.Printf("%v table created", AUTH_TABLE)

	// create tagunique table
	input = &dynamodb.CreateTableInput{
		AttributeDefinitions: []*dynamodb.AttributeDefinition{
			{
				AttributeName: aws.String("Id"),
				AttributeType: aws.String("S"),
			},
			{
				AttributeName: aws.String("Source"),
				AttributeType: aws.String("S"),
			},
		},
		KeySchema: []*dynamodb.KeySchemaElement{
			{
				AttributeName: aws.String("Id"),
				KeyType:       aws.String("HASH"),
			},
		},
		ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(settings.TableConfig.TagUnique.ProvisionedThroughput.ReadCapacity),
			WriteCapacityUnits: aws.Int64(settings.TableConfig.TagUnique.ProvisionedThroughput.WriteCapacity),
		},
		GlobalSecondaryIndexes: []*dynamodb.GlobalSecondaryIndex{
			{
				IndexName: aws.String("Source"),
				KeySchema: []*dynamodb.KeySchemaElement{
					{
						AttributeName: aws.String("Source"),
						KeyType:       aws.String("HASH"),
					},
				},
				Projection: &dynamodb.Projection{
					ProjectionType: aws.String("ALL"),
				},
				ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
					ReadCapacityUnits:  aws.Int64(settings.IndexConfig.Source.ProvisionedThroughput.ReadCapacity),
					WriteCapacityUnits: aws.Int64(settings.IndexConfig.Source.ProvisionedThroughput.WriteCapacity),
				},
			},
		},
		TableName: aws.String(TAGUNIQUE_TABLE),
	}
	_, err = a.svc.CreateTable(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() != dynamodb.ErrCodeResourceInUseException {
			log.Println(err)
			return err
		}
	}
	a.svc.WaitUntilTableExists(&dynamodb.DescribeTableInput{
		TableName: aws.String(TAGUNIQUE_TABLE),
	})
	log.Printf("%v table created", TAGUNIQUE_TABLE)

	// create subscriptions table
	input = &dynamodb.CreateTableInput{
		AttributeDefinitions: []*dynamodb.AttributeDefinition{
			{
				AttributeName: aws.String("Id"),
				AttributeType: aws.String("S"),
			},
			{
				AttributeName: aws.String("User"),
				AttributeType: aws.String("S"),
			},
			{
				AttributeName: aws.String("UpdatedAt"),
				AttributeType: aws.String("S"),
			},
			{
				AttributeName: aws.String("Topic"),
				AttributeType: aws.String("S"),
			},
		},
		KeySchema: []*dynamodb.KeySchemaElement{
			{
				AttributeName: aws.String("Id"),
				KeyType:       aws.String("HASH"),
			},
		},
		ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(settings.TableConfig.Subscriptions.ProvisionedThroughput.ReadCapacity),
			WriteCapacityUnits: aws.Int64(settings.TableConfig.Subscriptions.ProvisionedThroughput.WriteCapacity),
		},
		GlobalSecondaryIndexes: []*dynamodb.GlobalSecondaryIndex{
			{
				IndexName: aws.String("UserUpdatedAt"),
				KeySchema: []*dynamodb.KeySchemaElement{
					{
						AttributeName: aws.String("User"),
						KeyType:       aws.String("HASH"),
					},
					{
						AttributeName: aws.String("UpdatedAt"),
						KeyType:       aws.String("RANGE"),
					},
				},
				Projection: &dynamodb.Projection{
					ProjectionType: aws.String("ALL"),
				},
				ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
					ReadCapacityUnits:  aws.Int64(settings.IndexConfig.UserUpdatedAt.ProvisionedThroughput.ReadCapacity),
					WriteCapacityUnits: aws.Int64(settings.IndexConfig.UserUpdatedAt.ProvisionedThroughput.WriteCapacity),
				},
			},
			{
				IndexName: aws.String("Topic"),
				KeySchema: []*dynamodb.KeySchemaElement{
					{
						AttributeName: aws.String("Topic"),
						KeyType:       aws.String("HASH"),
					},
				},
				Projection: &dynamodb.Projection{
					ProjectionType: aws.String("ALL"),
				},
				ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
					ReadCapacityUnits:  aws.Int64(settings.IndexConfig.Topic.ProvisionedThroughput.ReadCapacity),
					WriteCapacityUnits: aws.Int64(settings.IndexConfig.Topic.ProvisionedThroughput.WriteCapacity),
				},
			},
		},
		TableName: aws.String(SUBSCRIPTIONS_TABLE),
	}
	_, err = a.svc.CreateTable(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() != dynamodb.ErrCodeResourceInUseException {
			log.Println(err)
			return err
		}
	}
	a.svc.WaitUntilTableExists(&dynamodb.DescribeTableInput{
		TableName: aws.String(SUBSCRIPTIONS_TABLE),
	})
	log.Printf("%v table created", SUBSCRIPTIONS_TABLE)

	// install self-talk service account
	user := &t.User{
		Access: t.DefaultAccess{
			Auth: t.ModeCP2P,
			Anon: t.ModeNone,
		},
		Public: map[string]string{
			"fn": "SelfTalkService",
		},
	}
	user.SetUid(SELF_TALK_SERVICE_USER_ID)
	item, err := dynamodbattribute.MarshalMap(user)
	if err != nil {
		return err
	}
	_, err = a.svc.PutItem(&dynamodb.PutItemInput{
		Item:      item,
		TableName: aws.String(USERS_TABLE),
	})
	if err != nil {
		log.Println(err)
		return err
	}
	log.Println("Successfully install self-talk service account")

	return nil
}

func (a *DynamoDBAdapter) UserCreate(user *t.User) (error, bool) {

	// insert tags
	if user.Tags != nil {
		type TagRecord struct {
			Id     string
			Source string
		}
		for _, tag := range user.Tags {
			tagRecord, err := dynamodbattribute.MarshalMap(TagRecord{Id: tag, Source: user.Id})
			if err != nil {
				log.Println(err)
				return err, false
			}
			_, err = a.svc.PutItem(&dynamodb.PutItemInput{
				Item:                tagRecord,
				TableName:           aws.String(TAGUNIQUE_TABLE),
				ConditionExpression: aws.String("attribute_not_exists(Id)"), //to ensure tag uniqueness
			})
			if err != nil {
				log.Println(err)
				return err, false
			}
		}
	}

	// insert user record to db
	item, err := dynamodbattribute.MarshalMap(*user)
	if err != nil {
		log.Println(err)
		return err, false
	}
	if *item["Devices"].NULL {
		item["Devices"] = &dynamodb.AttributeValue{M: map[string]*dynamodb.AttributeValue{}}
	}
	_, err = a.svc.PutItem(&dynamodb.PutItemInput{
		Item:                item,
		TableName:           aws.String(USERS_TABLE),
		ConditionExpression: aws.String("attribute_not_exists(Id)"),
	})
	if err != nil {
		log.Println(err)
		if aerr, ok := err.(awserr.Error); ok && (aerr.Code() == dynamodb.ErrCodeConditionalCheckFailedException) {
			return err, true
		}
		return err, false
	}
	return nil, false
}

func (a *DynamoDBAdapter) UserGet(uid t.Uid) (*t.User, error) {

	// get user from db
	kv, err := dynamodbattribute.MarshalMap(UserKey{Id: uid.String()})
	if err != nil {
		return nil, err
	}
	result, err := a.svc.GetItem(&dynamodb.GetItemInput{Key: kv, TableName: aws.String(USERS_TABLE)})
	if err != nil {
		return nil, err
	}

	// parse db result into t.User
	var user t.User
	if err = dynamodbattribute.UnmarshalMap(result.Item, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

func (a *DynamoDBAdapter) UserGetAll(uids ...t.Uid) ([]t.User, error) {
	// limit uids, not too good in this context maybe? --> but currently it used only for fetching p2p users
	if len(uids) > MAX_USERS_TO_FETCH {
		uids = uids[:MAX_USERS_TO_FETCH]
	}
	// construct keys
	var kv []map[string]*dynamodb.AttributeValue
	for _, uid := range uids {
		el, err := dynamodbattribute.MarshalMap(UserKey{uid.String()})
		if err == nil {
			kv = append(kv, el)
		}
	}
	// fetch items
	var items []map[string]*dynamodb.AttributeValue
	requestItems := map[string]*dynamodb.KeysAndAttributes{USERS_TABLE: {Keys: kv}}
	for len(requestItems) > 0 {
		result, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{RequestItems: requestItems})
		if err != nil {
			if len(items) > 0 {
				break
			} else {
				return nil, err
			}
		}
		items = append(items, result.Responses[USERS_TABLE]...)
		requestItems = result.UnprocessedKeys
	}
	// process items
	var users []t.User
	if err := dynamodbattribute.UnmarshalListOfMaps(items, &users); err != nil {
		return nil, err
	}
	return users, nil
}

func (a *DynamoDBAdapter) UserDelete(id t.Uid, soft bool) error {

	// prepare key
	kv, err := dynamodbattribute.MarshalMap(UserKey{id.String()})
	if err != nil {
		return err
	}

	if soft {
		// update DeletedAt & UpdatedAt fields
		type Eav struct {
			DeletedAt time.Time `json:":DeletedAt"`
			UpdatedAt time.Time `json:":UpdatedAt"`
		}
		now := t.TimeNow()
		eav, err := dynamodbattribute.MarshalMap(Eav{now, now})
		if err != nil {
			return err
		}
		_, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
			ExpressionAttributeValues: eav,
			Key:                       kv,
			TableName:                 aws.String(USERS_TABLE),
			UpdateExpression:          aws.String("set DeletedAt=:DeletedAt, UpdatedAt=:UpdatedAt"),
		})
		if err != nil {
			return err
		}
	} else {
		// literally delete row
		_, err = a.svc.DeleteItem(&dynamodb.DeleteItemInput{
			Key:       kv,
			TableName: aws.String(USERS_TABLE),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *DynamoDBAdapter) UserUpdateLastSeen(uid t.Uid, userAgent string, when time.Time) error {

	// prepare key
	kv, err := dynamodbattribute.MarshalMap(UserKey{uid.String()})
	if err != nil {
		return err
	}

	// prepare values
	type Eav struct {
		LastSeen  time.Time `json:":LastSeen"`
		UserAgent string    `json:":UserAgent"`
	}
	eav, err := dynamodbattribute.MarshalMap(Eav{when, userAgent})
	if err != nil {
		return err
	}

	// update item
	_, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
		ExpressionAttributeValues: eav,
		Key:                       kv,
		TableName:                 aws.String(USERS_TABLE),
		UpdateExpression:          aws.String("set LastSeen=:LastSeen, UserAgent=:UserAgent"),
	})
	return err
}

func (a *DynamoDBAdapter) ChangePassword(id t.Uid, password string) error {
	return errors.New("ChangePassword: not implemented")
}

func (a *DynamoDBAdapter) UserUpdate(uid t.Uid, update map[string]interface{}) error {

	// TODO: add tag re-indexing

	// prepare key
	kv, err := dynamodbattribute.MarshalMap(UserKey{Id: uid.String()})
	if err != nil {
		return err
	}

	// prepare values for update
	ean, eav, ue, err := parseEanEavUeUpdateItem(update)
	if err != nil {
		return err
	}

	// update item
	_, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
		Key:                       kv,
		TableName:                 aws.String(USERS_TABLE),
		ExpressionAttributeNames:  ean,
		ExpressionAttributeValues: eav,
		UpdateExpression:          ue,
	})
	if err != nil {
		return err
	}
	return nil
}

func (a *DynamoDBAdapter) GetAuthRecord(unique string) (t.Uid, int, []byte, time.Time, error) {

	// prepare key
	kv, err := dynamodbattribute.MarshalMap(AuthKey{unique})
	if err != nil {
		return t.ZeroUid, 0, nil, time.Time{}, err
	}

	// get item
	result, err := a.svc.GetItem(&dynamodb.GetItemInput{
		Key:                  kv,
		TableName:            aws.String(AUTH_TABLE),
		ProjectionExpression: aws.String("userid, secret, expires, authLvl"),
	})
	if err != nil {
		return t.ZeroUid, 0, nil, time.Time{}, err
	}

	// process result
	type Record struct {
		UserId  string    `json:"userid"`
		AuthLvl int       `json:"authLvl"`
		Secret  []byte    `json:"secret"`
		Expires time.Time `json:"expires"`
	}
	var record Record
	if err = dynamodbattribute.UnmarshalMap(result.Item, &record); err != nil {
		return t.ZeroUid, 0, nil, time.Time{}, err
	}
	return t.ParseUid(record.UserId), record.AuthLvl, record.Secret, record.Expires, nil
}

func (a *DynamoDBAdapter) AddAuthRecord(uid t.Uid, authLvl int, unique string, secret []byte, expires time.Time) (error, bool) {

	// prepare item
	item, err := dynamodbattribute.MarshalMap(map[string]interface{}{
		"unique":  unique,
		"userid":  uid.String(),
		"authLvl": authLvl,
		"secret":  secret,
		"expires": expires,
	})
	if err != nil {
		return err, false
	}

	// put item
	_, err = a.svc.PutItem(&dynamodb.PutItemInput{
		Item:      item,
		TableName: aws.String(AUTH_TABLE),
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && (aerr.Code() == dynamodb.ErrCodeConditionalCheckFailedException) {
			return err, true
		}
		return err, false
	}
	return nil, false
}

func (a *DynamoDBAdapter) DelAuthRecord(unique string) (int, error) {

	// prepare key
	kv, err := dynamodbattribute.MarshalMap(AuthKey{unique})
	if err != nil {
		return 0, err
	}

	// delete item
	_, err = a.svc.DeleteItem(&dynamodb.DeleteItemInput{
		Key:       kv,
		TableName: aws.String(AUTH_TABLE),
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func (a *DynamoDBAdapter) DelAllAuthRecords(uid t.Uid) (int, error) {

	// get all auth records for certain uid
	eav, err := dynamodbattribute.MarshalMap(map[string]string{
		":userid": uid.String(),
	})
	if err != nil {
		return 0, err
	}
	result, err := a.svc.Query(&dynamodb.QueryInput{
		ExpressionAttributeValues: eav,
		KeyConditionExpression:    aws.String("userid = :userid"),
		IndexName:                 aws.String("userid"),
		TableName:                 aws.String(AUTH_TABLE),
		ProjectionExpression:      aws.String("unique"),
	})
	if err != nil {
		return 0, err
	}
	var records []AuthKey
	if err = dynamodbattribute.UnmarshalListOfMaps(result.Items, &records); err != nil {
		return 0, err
	}

	// delete all records found
	var requests []*dynamodb.WriteRequest
	for _, record := range records {
		rv, err := dynamodbattribute.MarshalMap(record)
		if err == nil {
			el := &dynamodb.WriteRequest{
				DeleteRequest: &dynamodb.DeleteRequest{Key: rv},
			}
			requests = append(requests, el)
		}
	}
	_, err = a.svc.BatchWriteItem(&dynamodb.BatchWriteItemInput{
		RequestItems: map[string][]*dynamodb.WriteRequest{
			AUTH_TABLE: requests,
		},
	})
	if err != nil {
		return 0, err
	}
	return len(requests), nil
}

func (a *DynamoDBAdapter) UpdAuthRecord(unique string, authLvl int, secret []byte, expires time.Time) (int, error) {

	// prepare key
	kv, err := dynamodbattribute.MarshalMap(AuthKey{unique})
	if err != nil {
		return 0, err
	}

	// prepare values
	ean := map[string]*string{
		"#authLvl": aws.String("authLvl"),
		"#secret":  aws.String("secret"),
		"#expires": aws.String("expires"),
	}
	eav, err := dynamodbattribute.MarshalMap(map[string]interface{}{
		":authLvl": authLvl,
		":secret":  secret,
		":expires": expires,
	})
	if err != nil {
		return 0, err
	}

	// update item
	_, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
		ExpressionAttributeNames:  ean,
		ExpressionAttributeValues: eav,
		Key:                       kv,
		TableName:                 aws.String(AUTH_TABLE),
		UpdateExpression:          aws.String("set #authLvl = :authLvl, #secret = :secret, #expires = :expires"),
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func (a *DynamoDBAdapter) TopicCreate(topic *t.Topic) error {
	logDebugMessage(fmt.Sprintf("TopicCreate(topic: %v)", topic))
	item, err := dynamodbattribute.MarshalMap(topic)
	if err != nil {
		return err
	}
	_, err = a.svc.PutItem(&dynamodb.PutItemInput{
		Item:      item,
		TableName: aws.String(TOPICS_TABLE),
	})
	return err
}

func (a *DynamoDBAdapter) TopicCreateP2P(initiator, invited *t.Subscription) error {
	logDebugMessage(fmt.Sprintf("TopicCreateP2P(initiator: %v, invited: %v)", initiator, invited))
	// Don't care if the initiator changes own subscription
	initiator.Id = initiator.Topic + ":" + initiator.User
	item, err := dynamodbattribute.MarshalMap(initiator)
	if err != nil {
		return err
	}
	_, err = a.svc.PutItem(&dynamodb.PutItemInput{
		Item:      item,
		TableName: aws.String(SUBSCRIPTIONS_TABLE),
	})
	if err != nil {
		return err
	}

	// Ensure this is a new subscription. If one already exist, don't overwrite it
	invited.Id = invited.Topic + ":" + invited.User
	item, err = dynamodbattribute.MarshalMap(invited)
	if err != nil {
		return err
	}
	_, err = a.svc.PutItem(&dynamodb.PutItemInput{
		Item:                item,
		TableName:           aws.String(SUBSCRIPTIONS_TABLE),
		ConditionExpression: aws.String("attribute_not_exists(Id)"),
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() != dynamodb.ErrCodeConditionalCheckFailedException {
			return err
		}
	}

	// create topic
	topic := &t.Topic{ObjHeader: t.ObjHeader{Id: initiator.Topic}}
	topic.ObjHeader.MergeTimes(&initiator.ObjHeader)
	return a.TopicCreate(topic)
}

func (a *DynamoDBAdapter) TopicGet(topic string) (*t.Topic, error) {
	logDebugMessage(fmt.Sprintf("TopicGet(topic: %v)", topic))
	kv, err := dynamodbattribute.MarshalMap(TopicKey{topic})
	if err != nil {
		return nil, err
	}
	result, err := a.svc.GetItem(&dynamodb.GetItemInput{
		Key:       kv,
		TableName: aws.String(TOPICS_TABLE),
	})
	if err != nil {
		return nil, err
	}

	if len(result.Item) == 0 {
		return nil, nil
	}
	var t t.Topic
	if err = dynamodbattribute.UnmarshalMap(result.Item, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (a *DynamoDBAdapter) TopicsForUser(uid t.Uid, keepDeleted bool) ([]t.Subscription, error) {
	logDebugMessage(fmt.Sprintf("TopicsForUser(uid: %v, keepDeleted: %v)", uid, keepDeleted))
	// fetch all subscriptions owned by user
	eav, _ := dynamodbattribute.MarshalMap(map[string]interface{}{
		":User":     uid.String(),
		":MeTopic":  "usr" + uid.String(),
		":FndTopic": "fnd" + uid.String(),
	})
	input := &dynamodb.QueryInput{
		ExpressionAttributeNames: map[string]*string{
			"#User":  aws.String("User"),
			"#Topic": aws.String("Topic"),
		},
		ExpressionAttributeValues: eav,
		KeyConditionExpression:    aws.String("#User = :User"),
		FilterExpression:          aws.String("#Topic <> :MeTopic and #Topic <> :FndTopic"), // skip over `me` & `fnd` topics
		IndexName:                 aws.String("UserUpdatedAt"),
		TableName:                 aws.String(SUBSCRIPTIONS_TABLE),
	}
	if !keepDeleted {
		input.FilterExpression = aws.String("DeletedAt <> NOT_NULL")
	}
	result, err := a.svc.Query(input)
	if err != nil {
		return nil, fmt.Errorf("unable to fetch subscriptions for topics due: %v", err)
	}
	var items []map[string]*dynamodb.AttributeValue
	items = append(items, result.Items...)
	for len(result.LastEvaluatedKey) > 0 {
		input.ExclusiveStartKey = result.LastEvaluatedKey
		result, err = a.svc.Query(input)
		if err != nil {
			return nil, fmt.Errorf("unable to ftech more subscriptions for topics due: %v", err)
		}
		items = append(items, result.Items...)
	}

	// parse subscriptions result
	var subs []t.Subscription
	if err = dynamodbattribute.UnmarshalListOfMaps(items, &subs); err != nil {
		return nil, err
	}

	// define container for joining subscriptions info
	join := make(map[string]*t.Subscription)
	var topicsToFind []map[string]*dynamodb.AttributeValue
	var usersToFind []map[string]*dynamodb.AttributeValue
	for i := 0; i < len(subs); i++ {
		sub := &subs[i]
		tcat := t.GetTopicCat(sub.Topic)

		// only fetch info for p2p & grp topics
		if tcat == t.TopicCat_Me || tcat == t.TopicCat_Fnd {
			continue
		} else if tcat == t.TopicCat_P2P {
			uid1, uid2, _ := t.ParseP2P(sub.Topic)
			var peerUid t.Uid
			if uid1 == uid {
				peerUid = uid2
			} else {
				peerUid = uid1
			}
			uel, err := dynamodbattribute.MarshalMap(UserKey{peerUid.String()})
			if err != nil {
				return nil, err
			}
			usersToFind = append(usersToFind, uel)
		}
		tel, err := dynamodbattribute.MarshalMap(TopicKey{sub.Topic})
		if err != nil {
			return nil, err
		}
		topicsToFind = append(topicsToFind, tel)
		join[sub.Topic] = sub
	}

	var nProcess int
	errChan := make(chan error)

	// fetch topics data for completing basic info of p2p & grp topics
	if len(topicsToFind) > 0 {
		nProcess = int(math.Ceil(float64(len(topicsToFind)) / float64(MAX_BATCH_GET_ITEM)))
		for i := 0; i < nProcess; i++ {
			go func(i int) {
				var items []map[string]*dynamodb.AttributeValue
				startIndex := i * MAX_BATCH_GET_ITEM
				endIndex := startIndex + int(math.Min(float64(MAX_BATCH_GET_ITEM), float64(len(topicsToFind)-startIndex)))
				requestItems := map[string]*dynamodb.KeysAndAttributes{TOPICS_TABLE: {Keys: topicsToFind[startIndex:endIndex]}}
				for len(requestItems) > 0 {
					resTopics, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{RequestItems: requestItems})
					if err != nil {
						if len(items) > 0 {
							break
						} else {
							errChan <- fmt.Errorf("unable to fetch topics from %v to %v due %v", topicsToFind[startIndex], topicsToFind[endIndex-1], err)
							return
						}
					}
					items = append(items, resTopics.Responses[TOPICS_TABLE]...)
					requestItems = resTopics.UnprocessedKeys
				}
				var topics []t.Topic
				if err = dynamodbattribute.UnmarshalListOfMaps(items, &topics); err != nil {
					errChan <- fmt.Errorf("unable to parse topics result from %v to %v due %v", topicsToFind[startIndex], topicsToFind[endIndex-1], err)
					return
				}
				for i := 0; i < len(topics); i++ {
					top := &topics[i]
					sub := join[top.Id]
					sub.ObjHeader.MergeTimes(&top.ObjHeader)
					sub.SetSeqId(top.SeqId)
					sub.SetHardClearId(top.ClearId)
					if t.GetTopicCat(sub.Topic) == t.TopicCat_Grp {
						sub.SetPublic(top.Public)
					}
				}
				errChan <- nil
			}(i)
		}
		// wait for all process to be done
		for i := 0; i < nProcess; i++ {
			err = <-errChan
			if err != nil {
				// if any one of the process fail immediately exit
				return nil, err
			}
		}
	}
	// fetch users data for completing p2p info
	if len(usersToFind) > 0 {
		nProcess = int(math.Ceil(float64(len(usersToFind)) / float64(MAX_BATCH_GET_ITEM)))
		for i := 0; i < nProcess; i++ {
			go func(i int) {
				var items []map[string]*dynamodb.AttributeValue
				startIndex := i * MAX_BATCH_GET_ITEM
				endIndex := startIndex + int(math.Min(float64(MAX_BATCH_GET_ITEM), float64(len(usersToFind)-startIndex)))
				requestItems := map[string]*dynamodb.KeysAndAttributes{USERS_TABLE: {Keys: usersToFind[startIndex:endIndex]}}

				for len(requestItems) > 0 {
					resUsers, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{RequestItems: requestItems})
					if err != nil {
						if len(items) > 0 {
							break
						} else {
							errChan <- fmt.Errorf("unable to fetch users from %v to %v due %v", usersToFind[startIndex], usersToFind[endIndex-1], err)
							return
						}
					}
					items = append(items, resUsers.Responses[USERS_TABLE]...)
					requestItems = resUsers.UnprocessedKeys
				}
				var users []t.User
				if err = dynamodbattribute.UnmarshalListOfMaps(items, &users); err != nil {
					errChan <- fmt.Errorf("unable to parse users result due %v", err)
					return
				}
				for i := 0; i < len(users); i++ {
					usr := &users[i]
					uid2 := t.ParseUid(usr.Id)
					topic := uid.P2PName(uid2)
					if sub, ok := join[topic]; ok {
						sub.ObjHeader.MergeTimes(&usr.ObjHeader)
						sub.SetPublic(usr.Public)
						sub.SetWith(uid2.UserId())
						sub.SetDefaultAccess(usr.Access.Auth, usr.Access.Anon)
						sub.SetLastSeenAndUA(usr.LastSeen, usr.UserAgent)
					}
				}
				errChan <- nil
			}(i)
		}
		// wait for all process to be done
		for i := 0; i < nProcess; i++ {
			err = <-errChan
			if err != nil {
				// if any one of the process fail immediately exit
				return nil, err
			}
		}
	}
	return subs, nil
}

func (a *DynamoDBAdapter) UsersForTopic(topic string, keepDeleted bool) ([]t.Subscription, error) {
	logDebugMessage(fmt.Sprintf("UsersForTopic(topic: %v, keepDeleted: %v)", topic, keepDeleted))
	// get all subscriptions by topic
	eav, _ := dynamodbattribute.MarshalMap(map[string]string{":Topic": topic})
	input := &dynamodb.QueryInput{
		ExpressionAttributeValues: eav,
		IndexName:                 aws.String("Topic"),
		TableName:                 aws.String(SUBSCRIPTIONS_TABLE),
		KeyConditionExpression:    aws.String("Topic = :Topic"),
	}
	if !keepDeleted {
		input.FilterExpression = aws.String("DeletedAt <> NOT_NULL")
	}
	result, err := a.svc.Query(input)
	if err != nil {
		return nil, fmt.Errorf("unable to fetch subscriptions due: %v", err)
	}

	var items []map[string]*dynamodb.AttributeValue
	items = append(items, result.Items...)

	// attempt to get remaining subscriptions if any
	for len(result.LastEvaluatedKey) != 0 {
		input.ExclusiveStartKey = result.LastEvaluatedKey
		result, err = a.svc.Query(input)
		if err != nil {
			log.Println(fmt.Errorf("unable to fetch remaining subscriptions due: %v", err))
			break
		}
		items = append(items, result.Items...)
	}

	// parse subscriptions
	var subs []t.Subscription
	if err = dynamodbattribute.UnmarshalListOfMaps(items, &subs); err != nil {
		return nil, fmt.Errorf("unable to parse subscriptions due: %v", err)
	}

	// make container for joining subscriptions & user's public info
	join := make(map[string]*t.Subscription)
	var usersToLookUp []map[string]*dynamodb.AttributeValue
	for i := 0; i < len(subs); i++ {
		join[subs[i].User] = &subs[i]
		el, err := dynamodbattribute.MarshalMap(UserKey{subs[i].User})
		if err != nil {
			continue
		}
		usersToLookUp = append(usersToLookUp, el)
	}

	// attempt to fetch public value of users
	if len(usersToLookUp) > 0 {
		nProcess := int(math.Ceil(float64(len(usersToLookUp)) / float64(MAX_BATCH_GET_ITEM)))
		errChan := make(chan error)

		var err error
		for i := 0; i < nProcess; i++ {
			go func(i int) {
				var items []map[string]*dynamodb.AttributeValue
				startIndex := i * MAX_BATCH_GET_ITEM
				endIndex := startIndex + int(math.Min(float64(MAX_BATCH_GET_ITEM), float64(len(usersToLookUp)-startIndex)))
				requestItems := map[string]*dynamodb.KeysAndAttributes{USERS_TABLE: {Keys: usersToLookUp[startIndex:endIndex]}}

				for len(requestItems) > 0 {
					resUsers, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{RequestItems: requestItems})
					if err != nil {
						errChan <- fmt.Errorf("unable to fetch users public info due: %v", err)
						if len(items) > 0 {
							break
						} else {
							return
						}
					}
					items = append(items, resUsers.Responses[USERS_TABLE]...)
					requestItems = resUsers.UnprocessedKeys
				}
				var usrs []t.User
				if err = dynamodbattribute.UnmarshalListOfMaps(items, &usrs); err != nil {
					errChan <- fmt.Errorf("unable to parse responses of users public due: %v", err)
					return
				}
				// join result with main result
				for _, usr := range usrs {
					if sub, ok := join[usr.Id]; ok {
						sub.ObjHeader.MergeTimes(&usr.ObjHeader)
						sub.SetPublic(usr.Public)
					}
				}
				errChan <- nil
			}(i)
		}
		// wait for all process to complete
		for i := 0; i < nProcess; i++ {
			err = <-errChan
			if err != nil {
				log.Println(err)
			}
		}
	}
	return subs, nil
}

func (a *DynamoDBAdapter) TopicShare(shares []*t.Subscription) (int, error) {
	// assign ids + prepare write requests
	var requests []*dynamodb.WriteRequest
	for i := 0; i < len(shares); i++ {
		shares[i].Id = shares[i].Topic + ":" + shares[i].User
		item, err := dynamodbattribute.MarshalMap(shares[i])
		if err != nil {
			return 0, err
		}
		el := &dynamodb.WriteRequest{
			PutRequest: &dynamodb.PutRequest{
				Item: item,
			},
		}
		requests = append(requests, el)
	}
	// replace subscriptions
	_, err := a.svc.BatchWriteItem(&dynamodb.BatchWriteItemInput{
		RequestItems: map[string][]*dynamodb.WriteRequest{
			SUBSCRIPTIONS_TABLE: requests,
		},
	})
	if err != nil {
		return 0, nil
	}
	return len(shares), nil
}

func (a *DynamoDBAdapter) TopicDelete(topic string) error {
	// literally delete topic
	kv, err := dynamodbattribute.MarshalMap(TopicKey{topic})
	if err != nil {
		return err
	}
	_, err = a.svc.DeleteItem(&dynamodb.DeleteItemInput{
		Key:       kv,
		TableName: aws.String(TOPICS_TABLE),
	})
	return err
}

// update seqId, if `me`topic save update to users table, else to topics table
func (a *DynamoDBAdapter) TopicUpdateOnMessage(topic string, msg *t.Message) error {
	update := map[string]interface{}{
		"SeqId": msg.SeqId,
	}
	ean, eav, ue, err := parseEanEavUeUpdateItem(update)
	if err != nil {
		return err
	}

	var kv map[string]*dynamodb.AttributeValue
	input := &dynamodb.UpdateItemInput{
		ExpressionAttributeNames:  ean,
		ExpressionAttributeValues: eav,
		UpdateExpression:          ue,
	}
	var kObj interface{}

	if strings.HasPrefix(topic, "usr") {
		kObj = UserKey{t.ParseUserId(topic).String()}
		input.TableName = aws.String(USERS_TABLE)
	} else {
		kObj = TopicKey{topic}
		input.TableName = aws.String(TOPICS_TABLE)
	}

	kv, err = dynamodbattribute.MarshalMap(kObj)
	if err != nil {
		return err
	}
	input.Key = kv
	_, err = a.svc.UpdateItem(input)
	return err
}

func (a *DynamoDBAdapter) TopicUpdate(topic string, update map[string]interface{}) error {
	kv, err := dynamodbattribute.MarshalMap(TopicKey{topic})
	if err != nil {
		return err
	}
	ean, eav, ue, err := parseEanEavUeUpdateItem(update)
	if err != nil {
		return err
	}
	_, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
		Key:                       kv,
		TableName:                 aws.String(TOPICS_TABLE),
		ExpressionAttributeNames:  ean,
		ExpressionAttributeValues: eav,
		UpdateExpression:          ue,
	})
	return err
}

func (a *DynamoDBAdapter) SubscriptionGet(topic string, user t.Uid) (*t.Subscription, error) {
	var sub t.Subscription
	kv, _ := dynamodbattribute.MarshalMap(SubscriptionKey{topic + ":" + user.String()})
	result, err := a.svc.GetItem(&dynamodb.GetItemInput{
		Key:       kv,
		TableName: aws.String(SUBSCRIPTIONS_TABLE),
	})
	if err != nil {
		return nil, err
	} else if err = dynamodbattribute.UnmarshalMap(result.Item, &sub); err != nil {
		return nil, err
	}
	return &sub, nil
}

func (a *DynamoDBAdapter) SubsForUser(forUser t.Uid, keepDeleted bool) ([]t.Subscription, error) {
	logDebugMessage(fmt.Sprintf("SubsForUser(forUser: %v, keepDeleted: %v)", forUser, keepDeleted))
	if forUser.IsZero() {
		return nil, errors.New("Invalid user ID in SubsForUser")
	}

	eav, err := dynamodbattribute.MarshalMap(map[string]string{
		":User": forUser.String(),
	})
	if err != nil {
		return nil, err
	}
	input := &dynamodb.QueryInput{
		ExpressionAttributeNames: map[string]*string{
			"#User": aws.String("User"),
		},
		ExpressionAttributeValues: eav,
		KeyConditionExpression:    aws.String("#User = :User"),
		IndexName:                 aws.String("UserUpdatedAt"),
		TableName:                 aws.String(SUBSCRIPTIONS_TABLE),
	}
	if !keepDeleted {
		input.FilterExpression = aws.String("DeletedAt <> NOT_NULL")
	}
	result, err := a.svc.Query(input)
	if err != nil {
		return nil, fmt.Errorf("unable to fetch subscription due: %v", err)
	}

	var items []map[string]*dynamodb.AttributeValue
	items = append(items, result.Items...)
	for len(result.LastEvaluatedKey) > 0 {
		input.ExclusiveStartKey = result.LastEvaluatedKey
		result, err = a.svc.Query(input)
		if err != nil {
			log.Println(fmt.Errorf("unable to fetch more subscription due: %v", err))
			break
		}
		items = append(items, result.Items...)
	}

	var subs []t.Subscription
	if err = dynamodbattribute.UnmarshalListOfMaps(items, &subs); err != nil {
		return nil, err
	}
	return subs, nil
}

func (a *DynamoDBAdapter) SubsForTopic(topic string, keepDeleted bool) ([]t.Subscription, error) {
	logDebugMessage(fmt.Sprintf("SubsForTopic(topic: %v, keepDeleted: %v)", topic, keepDeleted))
	// must load User.Public for p2p topics
	var p2p []t.User
	var err error
	if t.GetTopicCat(topic) == t.TopicCat_P2P {
		uid1, uid2, _ := t.ParseP2P(topic)
		if p2p, err = a.UserGetAll(uid1, uid2); err != nil {
			return nil, err
		} else if len(p2p) != 2 {
			return nil, errors.New("failed to load two p2p users")
		}
	}
	// get subscriptions by topic
	eav, _ := dynamodbattribute.MarshalMap(map[string]string{":Topic": topic})
	input := &dynamodb.QueryInput{
		ExpressionAttributeValues: eav,
		KeyConditionExpression:    aws.String("Topic = :Topic"),
		IndexName:                 aws.String("Topic"),
		TableName:                 aws.String(SUBSCRIPTIONS_TABLE),
	}
	if !keepDeleted {
		input.FilterExpression = aws.String("DeletedAt <> NOT_NULL")
	}
	result, err := a.svc.Query(input)
	if err != nil {
		return nil, fmt.Errorf("unable to fetch subscriptions due: %v", err)
	}
	var items []map[string]*dynamodb.AttributeValue
	items = append(items, result.Items...)
	for len(result.LastEvaluatedKey) > 0 {
		input.ExclusiveStartKey = result.LastEvaluatedKey
		result, err = a.svc.Query(input)
		if err != nil {
			log.Println(fmt.Errorf("unable to fetch more subscriptions due: %v", err))
			break
		}
		items = append(items, result.Items...)
	}

	// parse result
	var subs []t.Subscription
	if err = dynamodbattribute.UnmarshalListOfMaps(items, &subs); err != nil {
		return nil, err
	}
	for i := 0; i < len(subs); i++ {
		if p2p != nil {
			// Assigning values provided by the other user
			if p2p[0].Id == subs[i].User {
				subs[i].SetPublic(p2p[1].Public)
				subs[i].SetWith(p2p[1].Id)
				subs[i].SetDefaultAccess(p2p[1].Access.Auth, p2p[1].Access.Anon)
			} else {
				subs[i].SetPublic(p2p[0].Public)
				subs[i].SetWith(p2p[0].Id)
				subs[i].SetDefaultAccess(p2p[0].Access.Auth, p2p[0].Access.Anon)
			}
		}
	}
	return subs, nil
}

func (a *DynamoDBAdapter) SubsUpdate(topic string, user t.Uid, update map[string]interface{}) error {
	kv, err := dynamodbattribute.MarshalMap(SubscriptionKey{topic + ":" + user.String()})
	if err != nil {
		return err
	}
	ean, eav, ue, err := parseEanEavUeUpdateItem(update)
	if err != nil {
		return err
	}
	_, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
		Key:                       kv,
		TableName:                 aws.String(SUBSCRIPTIONS_TABLE),
		ExpressionAttributeNames:  ean,
		ExpressionAttributeValues: eav,
		UpdateExpression:          ue,
	})
	return err
}

func (a *DynamoDBAdapter) SubsDelete(topic string, user t.Uid) error {
	// update UpdateAt & DeletedAt user's subscription
	kv, err := dynamodbattribute.MarshalMap(&SubscriptionKey{topic + ":" + user.String()})
	if err != nil {
		return err
	}
	now := t.TimeNow()
	eav, _ := dynamodbattribute.MarshalMap(map[string]interface{}{":UpdatedAt": now, ":DeletedAt": now})

	_, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
		ExpressionAttributeValues: eav,
		Key:                       kv,
		TableName:                 aws.String(SUBSCRIPTIONS_TABLE),
		UpdateExpression:          aws.String("set UpdatedAt = :UpdatedAt, DeletedAt = :DeletedAt"),
	})
	return err
}

func (a *DynamoDBAdapter) SubsDelForTopic(topic string) error {

	// get subscription ids
	eav, _ := dynamodbattribute.MarshalMap(map[string]string{":Topic": topic})
	input := &dynamodb.QueryInput{
		ExpressionAttributeNames: map[string]*string{
			"#User": aws.String("User"),
		},
		ExpressionAttributeValues: eav,
		KeyConditionExpression:    aws.String("Topic = :Topic"),
		IndexName:                 aws.String("Topic"),
		TableName:                 aws.String(SUBSCRIPTIONS_TABLE),
		ProjectionExpression:      aws.String("#User"),
	}
	result, err := a.svc.Query(input)
	if err != nil {
		return fmt.Errorf("unable to fetch subscriptions for topic %v due: %v", topic, err)
	}
	var items []map[string]*dynamodb.AttributeValue
	items = append(items, result.Items...)

	for len(result.LastEvaluatedKey) != 0 {
		input.ExclusiveStartKey = result.LastEvaluatedKey
		result, err = a.svc.Query(input)
		if err != nil {
			break
		}
		items = append(items, result.Items...)
	}

	// delete each subscriptions
	if len(items) > 0 {
		var records []UserKey
		if err = dynamodbattribute.UnmarshalListOfMaps(items, &records); err != nil {
			return fmt.Errorf("unable to parse subscription result due: %v", err)
		}

		// do parallel processing
		errChan := make(chan error)
		for _, record := range records {
			go func(user string) {
				errChan <- a.SubsDelete(topic, t.ParseUid(user))
			}(record.Id)
		}
		for range records {
			err = <-errChan
			if err != nil {
				log.Println(err)
			}
		}
	}
	return nil
}

func (a *DynamoDBAdapter) FindSubs(uid t.Uid, query []interface{}) ([]t.Subscription, error) {
	logDebugMessage(fmt.Sprintf("FindSubs(uid: %v, query: %v)", uid, query))
	uniqueIdx := make(map[string]bool) // to ensure uniqueness of tag & userid

	// get user id from tagunique for each tag in query
	var tkvs []map[string]*dynamodb.AttributeValue
	for _, q := range query {
		if tag, ok := q.(string); ok {
			if !uniqueIdx[tag] {
				kv, err := dynamodbattribute.MarshalMap(TagUniqueKey{tag})
				if err != nil {
					return nil, err
				}
				tkvs = append(tkvs, kv)
				uniqueIdx[tag] = true
			}
		}
	}
	// limit tags
	if len(tkvs) > MAX_FIND_SUBS_RESULT {
		tkvs = tkvs[:MAX_FIND_SUBS_RESULT]
	}

	var itemsTag []map[string]*dynamodb.AttributeValue
	requestItemsTag := map[string]*dynamodb.KeysAndAttributes{TAGUNIQUE_TABLE: {Keys: tkvs}}
	for len(requestItemsTag) > 0 {
		resTag, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{RequestItems: requestItemsTag})
		if err != nil {
			if len(itemsTag) > 0 {
				break
			} else {
				return nil, err
			}
		}
		itemsTag = append(itemsTag, resTag.Responses[TAGUNIQUE_TABLE]...)
		requestItemsTag = resTag.UnprocessedKeys
	}
	type Record struct {
		Tag    string `json:"Id"`
		UserId string `json:"Source"`
	}
	var records []Record
	if err := dynamodbattribute.UnmarshalListOfMaps(itemsTag, &records); err != nil {
		return nil, err
	}

	// build unique users info to fetch
	var usersToFind []map[string]*dynamodb.AttributeValue
	userTagMap := make(map[string]string)
	for _, record := range records {
		// ensure uniqueness of user id in result
		if !uniqueIdx[record.UserId] {
			kv, err := dynamodbattribute.MarshalMap(UserKey{record.UserId})
			if err != nil {
				continue
			}
			usersToFind = append(usersToFind, kv)
			userTagMap[record.UserId] = record.Tag
			uniqueIdx[record.UserId] = true
		}
	}

	// fetch users for completing subscriptions info
	var itemsUser []map[string]*dynamodb.AttributeValue
	requestItemsUser := map[string]*dynamodb.KeysAndAttributes{USERS_TABLE: {Keys: usersToFind}}
	for len(requestItemsUser) > 0 {
		resUsers, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{RequestItems: requestItemsUser})
		if err != nil {
			if len(itemsUser) > 0 {
				break
			} else {
				return nil, err
			}
		}
		itemsUser = append(itemsUser, resUsers.Responses[USERS_TABLE]...)
		requestItemsUser = resUsers.UnprocessedKeys
	}
	// parse result
	var users []t.User
	if err := dynamodbattribute.UnmarshalListOfMaps(itemsUser, &users); err != nil {
		return nil, err
	}
	var subs []t.Subscription
	for _, user := range users {
		if user.Id == uid.String() {
			// Skip the callee
			continue
		}
		var sub t.Subscription
		sub.CreatedAt = user.CreatedAt
		sub.UpdatedAt = user.UpdatedAt
		sub.User = user.Id
		sub.SetPublic(user.Public)
		sub.Private = []string{userTagMap[user.Id]}
		subs = append(subs, sub)
	}
	return subs, nil
}

func (a *DynamoDBAdapter) MessageSave(msg *t.Message) error {

	eLog := ErrorLogger{"MessageSave"}
	msg.SetUid(store.GetUid())
	item, err := dynamodbattribute.MarshalMap(msg)
	if err != nil {
		eLog.LogError(err)
		return err
	}

	if *item["DeletedFor"].NULL == true {
		item["DeletedFor"] = &dynamodb.AttributeValue{L: []*dynamodb.AttributeValue{}}
	}

	// set expire duration
	expireDurationInSeconds := EXPIRE_DURATION_MESSAGE_ME
	switch t.GetTopicCat(msg.Topic) {
	case t.TopicCat_P2P:
		expireDurationInSeconds = EXPIRE_DURATION_MESSAGE_P2P
	case t.TopicCat_Grp:
		expireDurationInSeconds = EXPIRE_DURATION_MESSAGE_GROUP
	}
	expireTimeUnix := time.Now().UTC().Add(time.Duration(expireDurationInSeconds) * time.Second).Unix()
	item["ExpireTime"] = &dynamodb.AttributeValue{N: aws.String(fmt.Sprintf("%d", expireTimeUnix))}

	_, err = a.svc.PutItem(&dynamodb.PutItemInput{
		Item:      item,
		TableName: aws.String(MESSAGES_TABLE),
	})
	if err != nil {
		eLog.LogError(err)
	}
	return err
}

// ini nanti pattern fetch message perlu dijelaskan ke k.dimas sm k.yacob
// ini perlu di test dgn payload message yg banyak
func (a *DynamoDBAdapter) MessageGetAll(topic string, forUser t.Uid, opts *t.BrowseOpt) ([]t.Message, error) {
	logDebugMessage(fmt.Sprintf("MessageGetAll(topic: %v, forUser: %v, opts: %v)", topic, forUser, opts))
	since := 0
	before := math.MaxInt32
	numMessagesRetrieved := uint(MAX_MESSAGES_RETRIEVED)

	if opts != nil {
		if opts.Since > 0 {
			since = opts.Since
		}
		if opts.Before > 0 {
			before = opts.Before
		}
		if opts.Limit > 0 && opts.Limit < numMessagesRetrieved {
			numMessagesRetrieved = opts.Limit
		}
	}

	eav, err := dynamodbattribute.MarshalMap(map[string]interface{}{
		":Topic":  topic,
		":Since":  since,
		":Before": before,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to parse expression attribute values due: %v", err)
	}

	result, err := a.svc.Query(&dynamodb.QueryInput{
		ExpressionAttributeValues: eav,
		KeyConditionExpression:    aws.String("Topic = :Topic and SeqId between :Since and :Before"),
		TableName:                 aws.String(MESSAGES_TABLE),
		Limit:                     aws.Int64(int64(numMessagesRetrieved)),
		ScanIndexForward:          aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("unable fetch items due: %v", err)
	}
	var items []map[string]*dynamodb.AttributeValue
	items = append(items, result.Items...)

	itemLeft := int(numMessagesRetrieved) - len(items)
	for itemLeft > 0 && len(result.LastEvaluatedKey) != 0 {
		result, err = a.svc.Query(&dynamodb.QueryInput{
			ExpressionAttributeValues: eav,
			KeyConditionExpression:    aws.String("Topic = :Topic and SeqId between :Since and :Before"),
			TableName:                 aws.String(MESSAGES_TABLE),
			Limit:                     aws.Int64(int64(itemLeft)),
			ExclusiveStartKey:         result.LastEvaluatedKey,
			ScanIndexForward:          aws.Bool(false),
		})
		if err != nil {
			log.Println(fmt.Errorf("unable to fetch remaining items due to: %v, last evaluated key: %v", err, result.LastEvaluatedKey))
			break
		}
		items = append(items, result.Items...)
		itemLeft = int(numMessagesRetrieved) - len(items) // update just in case there dynamodb make pagination again
	}

	var msgs []t.Message
	if err = dynamodbattribute.UnmarshalListOfMaps(items, &msgs); err != nil {
		return nil, fmt.Errorf("unable to marshal items into []t.Message due: %v", err)
	}

	requester := forUser.String()
	for i := 0; i < len(msgs); i++ {
		if msgs[i].DeletedFor != nil {
			for j := 0; j < len(msgs[i].DeletedFor); j++ {
				if msgs[i].DeletedFor[j].User == requester {
					msgs[i].DeletedAt = &msgs[i].DeletedFor[j].Timestamp
					break
				}
			}
		}
	}
	return msgs, nil
}

func (a *DynamoDBAdapter) MessageDeleteAll(topic string, before int) error {
	/*
	   It is possible for `before` value to be negative in which means user
	   want to delete all messages on that topic.

	   However in dynamodb such operation is hard to do. So the solution is
	   by updating ClearId of each topic type. Then leave the messages to be
	   expired by themselves.

	   ClearId location for each topic type:
	   - grp => topics.ClearId
	   - me => users.ClearId
	   - p2p => subscriptions.ClearId
	*/

	ue, ce := aws.String("set ClearId = :ClearId"), aws.String("attribute_exists(Id)")
	eav, err := dynamodbattribute.MarshalMap(map[string]interface{}{
		":ClearId": before,
	})
	if err != nil {
		return err
	}
	// process based on topic type
	switch tcat := t.GetTopicCat(topic); tcat {
	case t.TopicCat_Me:
		uid := t.ParseUserId(topic)
		kv, err := dynamodbattribute.MarshalMap(UserKey{uid.String()})
		if err != nil {
			return err
		}
		_, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
			ExpressionAttributeValues: eav,
			Key:                       kv,
			TableName:                 aws.String(USERS_TABLE),
			UpdateExpression:          ue,
			ConditionExpression:       ce,
		})
		if err != nil {
			if aerr, ok := err.(awserr.Error); ok && (aerr.Code() == dynamodb.ErrCodeConditionalCheckFailedException) {
				return nil
			}
			return err
		}
		return nil
	case t.TopicCat_Grp:
		kv, err := dynamodbattribute.MarshalMap(TopicKey{topic})
		if err != nil {
			return err
		}
		_, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
			ExpressionAttributeValues: eav,
			Key:                       kv,
			TableName:                 aws.String(TOPICS_TABLE),
			UpdateExpression:          ue,
			ConditionExpression:       ce,
		})
		if err != nil {
			if aerr, ok := err.(awserr.Error); ok && (aerr.Code() == dynamodb.ErrCodeConditionalCheckFailedException) {
				return nil
			}
			return err
		}
		return nil
	case t.TopicCat_P2P:
		uid1, uid2, err := t.ParseP2P(topic)
		if err != nil {
			return err
		}
		subKeys := []SubscriptionKey{
			SubscriptionKey{topic + ":" + uid1.String()},
			SubscriptionKey{topic + ":" + uid2.String()},
		}
		for _, subKey := range subKeys {
			kv, err := dynamodbattribute.MarshalMap(subKey)
			if err != nil {
				return err
			}
			_, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
				ExpressionAttributeValues: eav,
				Key:                       kv,
				TableName:                 aws.String(SUBSCRIPTIONS_TABLE),
				UpdateExpression:          ue,
				ConditionExpression:       ce,
			})
			if err != nil {
				if aerr, ok := err.(awserr.Error); ok && (aerr.Code() == dynamodb.ErrCodeConditionalCheckFailedException) {
					continue
				}
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

func (a *DynamoDBAdapter) MessageDeleteList(topic string, forUser t.Uid, hard bool, list []int) error {
	// do parallel update using goroutine for faster operation

	var errResult error
	errCh := make(chan error)
	for _, seqId := range list {
		go func(seqId int) {
			kv, err := dynamodbattribute.MarshalMap(MessageKey{topic, seqId})
			if err != nil {
				errCh <- err
				return
			}

			var eav map[string]*dynamodb.AttributeValue
			var ue *string

			if hard {
				// hard == true, set DeletedAt to now
				eav, err = dynamodbattribute.MarshalMap(map[string]interface{}{
					":DeletedAt": t.TimeNow(),
				})
				ue = aws.String("set DeletedAt = :DeletedAt")
			} else {
				// hard == false, append current user id to DeletedFor along with timestamp
				eav, err = dynamodbattribute.MarshalMap(map[string]interface{}{
					":DeletedFor": t.SoftDelete{
						User:      forUser.String(),
						Timestamp: t.TimeNow(),
					},
				})
				ue = aws.String("set DeletedFor[999999999] = :DeletedFor")
			}

			if err != nil {
				errCh <- err
				return
			}
			_, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
				ExpressionAttributeValues: eav,
				Key:                       kv,
				TableName:                 aws.String(MESSAGES_TABLE),
				UpdateExpression:          ue,
			})
			if err != nil {
				errCh <- err
				return
			}
			errCh <- nil
		}(seqId)
	}

	// wait for all goroutine to complete
	for i := 0; i < len(list); i++ {
		errResult = <-errCh
	}
	return errResult
}

func deviceHasher(deviceId string) string {
	// Generate custom key as [64-bit hash of device id] to ensure predictable
	// length of the key
	hasher := fnv.New64()
	hasher.Write([]byte(deviceId))
	return strconv.FormatUint(uint64(hasher.Sum64()), 16)
}

func (a *DynamoDBAdapter) DeviceUpsert(uid t.Uid, dev *t.DeviceDef) error {
	// prepare hash
	hash := deviceHasher(dev.DeviceId)
	// prepare key
	kv, err := dynamodbattribute.MarshalMap(UserKey{uid.String()})
	if err != nil {
		return err
	}
	// prepare ean, eav, ue
	ean := map[string]*string{"#device": aws.String(hash)}
	eav, err := dynamodbattribute.MarshalMap(map[string]*t.DeviceDef{":device": dev})
	if err != nil {
		return err
	}
	ue := aws.String("SET Devices.#device = :device")
	_, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
		ExpressionAttributeNames:  ean,
		ExpressionAttributeValues: eav,
		Key:                       kv,
		TableName:                 aws.String(USERS_TABLE),
		UpdateExpression:          ue,
	})
	return err
}

// TODO: need better handling of batch get item
func (a *DynamoDBAdapter) DeviceGetAll(uids ...t.Uid) (map[t.Uid][]t.DeviceDef, int, error) {
	// limit uids
	if len(uids) > MAX_DEVICES_PER_USER {
		uids = uids[:MAX_DEVICES_PER_USER]
	}
	// get devices for each uid
	var kvs []map[string]*dynamodb.AttributeValue
	for _, uid := range uids {
		el, err := dynamodbattribute.MarshalMap(UserKey{uid.String()})
		if err != nil {
			kvs = append(kvs, el)
		}
	}

	var items []map[string]*dynamodb.AttributeValue
	requestItems := map[string]*dynamodb.KeysAndAttributes{USERS_TABLE: {Keys: kvs, ProjectionExpression: aws.String("Id, Devices")}}
	for len(requestItems) > 0 {
		resUsers, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{RequestItems: requestItems})
		if err != nil {
			if len(items) > 0 {
				break
			} else {
				return nil, 0, err
			}
		}
		items = append(items, resUsers.Responses[USERS_TABLE]...)
		requestItems = resUsers.UnprocessedKeys
	}
	type Record struct {
		Id      string
		Devices map[string]*t.DeviceDef
	}
	var records []Record
	if err := dynamodbattribute.UnmarshalListOfMaps(items, &records); err != nil {
		return nil, 0, err
	}

	// convert devices map into list for each record, then put it on container map
	result := make(map[t.Uid][]t.DeviceDef)
	count := 0
	var uid t.Uid
	for _, record := range records {
		if len(record.Devices) > 0 {
			if err := uid.UnmarshalText([]byte(record.Id)); err != nil {
				log.Print(err.Error())
				continue
			}

			result[uid] = make([]t.DeviceDef, len(record.Devices))
			i := 0
			for _, def := range record.Devices {
				if def != nil {
					result[uid][i] = *def
					i++
					count++
				}
			}
		}
	}
	return result, count, nil
}

func (a *DynamoDBAdapter) DeviceDelete(uid t.Uid, deviceId string) error {
	// prepare hash
	hash := deviceHasher(deviceId)
	// prepare key
	kv, err := dynamodbattribute.MarshalMap(UserKey{uid.String()})
	if err != nil {
		return err
	}
	// prepare ean, ue
	ean := map[string]*string{"#device": aws.String(hash)}
	ue := aws.String("REMOVE Devices.#device")
	_, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
		ExpressionAttributeNames: ean,
		Key:                      kv,
		TableName:                aws.String(USERS_TABLE),
		UpdateExpression:         ue,
	})
	return err
}

func init() {
	store.Register("dynamodb", &DynamoDBAdapter{})
}
