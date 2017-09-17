package dynamodb

import (
    "errors"
    "time"
    "strings"
    "math"
    "fmt"
    "hash/fnv"
    "strconv"
    "log"
    "encoding/json"
    
    t "github.com/tinode/chat/server/store/types"
    "github.com/tinode/chat/server/store"
    "github.com/aws/aws-sdk-go/aws"
    "github.com/aws/aws-sdk-go/aws/session"
    "github.com/aws/aws-sdk-go/aws/awserr"
    "github.com/aws/aws-sdk-go/service/dynamodb"
    "github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
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

const (
    USERS_TABLE string = "TinodeUsers"
    AUTH_TABLE string = "TinodeAuth"
    TAGUNIQUE_TABLE string = "TinodeTagUnique"
    TOPICS_TABLE string = "TinodeTopics"
    SUBSCRIPTIONS_TABLE string = "TinodeSubscriptions"
    MESSAGES_TABLE string = "TinodeMessages"
    MAX_RESULTS int = 100
    MAX_DELETE_ITEMS int = 25
    MAX_MESSAGES_RETRIEVED int = 1024 // max messages retrieved in single get messages operation
    
    EXPIRE_DURATION_MESSAGE_GROUP int = 604800 // 1 week
    EXPIRE_DURATION_MESSAGE_ME int = 2592000// 1 month
    EXPIRE_DURATION_MESSAGE_P2P int = 31536000 // 1 year
)

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
    
    type Settings struct {
        Region string       `json:"region"`
        Endpoint string     `json:"endpoint"`
        Profile string      `json:"profile"`
    }
    var settings Settings
    if err := json.Unmarshal([]byte(jsonstring), &settings); err != nil {
        return err
    }
    sess, err := session.NewSessionWithOptions(session.Options{
        Config: aws.Config{
            Region: aws.String(settings.Region),
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
    return errors.New("CreateDb: not implemented")
}

func (a *DynamoDBAdapter) UserCreate(user *t.User) (error, bool) {
    
    // insert tags
    if user.Tags != nil {
        type TagRecord struct {
            Id string
            Source string
        }
        for _, tag := range user.Tags {
            tagRecord, err := dynamodbattribute.MarshalMap(TagRecord{Id: tag, Source: user.Id})
            if err != nil {
                log.Println(err)
                return err, false
            }
            _, err = a.svc.PutItem(&dynamodb.PutItemInput{
                Item: tagRecord,
                TableName: aws.String(TAGUNIQUE_TABLE),
                ConditionExpression: aws.String("attribute_not_exists(Id)"), //to ensure tag uniqueness
            })
            if err != nil {
                log.Println(err)
                return err, false
            }
        }
    }
    
    // set devices type to empty object if nil, unique to dynamodb
    if user.Devices == nil {
        user.Devices = map[string]*t.DeviceDef{}
    }
    
    // insert user record to db
    userRecord, err := dynamodbattribute.MarshalMap(*user)
    if err != nil {
        log.Println(err)
        return err, false
    }
    _, err = a.svc.PutItem(&dynamodb.PutItemInput{
        Item: userRecord,
        TableName: aws.String(USERS_TABLE),
        ConditionExpression: aws.String("attribute_not_exists(Id)"),
    })
    if err != nil {
        log.Println(err)
        if aerr, ok := err.(awserr.Error); ok && (aerr.Code() == dynamodb.ErrCodeConditionalCheckFailedException){
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
    result, err := a.svc.GetItem(&dynamodb.GetItemInput{ Key: kv, TableName: aws.String(USERS_TABLE) })
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
    
    // construct keys
    var kv []map[string]*dynamodb.AttributeValue
    for _, uid := range uids {
        el, err := dynamodbattribute.MarshalMap(UserKey{uid.String()})
        if err == nil {
            kv = append(kv, el)
        }
    }
    // fetch items
    result, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{
        RequestItems: map[string]*dynamodb.KeysAndAttributes{USERS_TABLE:{Keys: kv}},
    })
    if err != nil {
        return nil, err
    }
    // process items
    var users []t.User
    if err = dynamodbattribute.UnmarshalListOfMaps(result.Responses[USERS_TABLE], &users); err != nil {
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
            Key: kv,
            TableName: aws.String(USERS_TABLE),
            UpdateExpression: aws.String("set DeletedAt=:DeletedAt, UpdatedAt=:UpdatedAt"),
        })
        if err != nil {
            return err
        }
    } else {
        // literally delete row
        _, err = a.svc.DeleteItem(&dynamodb.DeleteItemInput{
            Key: kv,
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
        LastSeen time.Time `json:":LastSeen"`
        UserAgent string   `json:":UserAgent"`
    }
    eav, err := dynamodbattribute.MarshalMap(Eav{when, userAgent})
    if err != nil {
        return err
    }
    
    // update item
    _, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
        ExpressionAttributeValues: eav,
        Key: kv,
        TableName: aws.String(USERS_TABLE),
        UpdateExpression: aws.String("set LastSeen=:LastSeen, UserAgent=:UserAgent"),
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
        Key: kv,
        TableName: aws.String(USERS_TABLE),
        ExpressionAttributeNames: ean,
        ExpressionAttributeValues: eav,
        UpdateExpression: ue,
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
        Key: kv,
        TableName: aws.String(AUTH_TABLE),
        ProjectionExpression: aws.String("userid, secret, expires, authLvl"),
    })
    if err != nil {
        return t.ZeroUid, 0, nil, time.Time{}, err
    }
    
    // process result
    type Record struct {
        UserId string       `json:"userid"`
        AuthLvl int         `json:"authLvl"`
        Secret []byte       `json:"secret"`
        Expires time.Time   `json:"expires"`
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
        "unique": unique,
        "userid": uid.String(),
        "authLvl": authLvl,
        "secret": secret,
        "expires": expires,
    })
    if err != nil {
        return err, false
    }
    
    // put item
    _, err = a.svc.PutItem(&dynamodb.PutItemInput{
        Item: item,
        TableName: aws.String(AUTH_TABLE),
    })
    if err != nil {
        if aerr, ok := err.(awserr.Error); ok && (aerr.Code() == dynamodb.ErrCodeConditionalCheckFailedException){
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
        Key: kv,
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
        KeyConditionExpression: aws.String("userid = :userid"),
        IndexName: aws.String("userid"),
        TableName: aws.String(AUTH_TABLE),
        ProjectionExpression: aws.String("unique"),
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
    ean := map[string]*string {
        "#authLvl": aws.String("authLvl"),
        "#secret": aws.String("secret"),
        "#expires": aws.String("expires"),
    }
    eav, err := dynamodbattribute.MarshalMap(map[string]interface{}{
        ":authLvl": authLvl,
        ":secret": secret,
        ":expires": expires,
    })
    if err != nil {
        return 0, err
    }
    
    // update item
    _, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
        ExpressionAttributeNames: ean,
        ExpressionAttributeValues: eav,
        Key: kv,
        TableName: aws.String(AUTH_TABLE),
        UpdateExpression: aws.String("set #authLvl = :authLvl, #secret = :secret, #expires = :expires"),
    })
    if err != nil {
        return 0, err
    }
    return 1, nil
}

func (a *DynamoDBAdapter) TopicCreate(topic *t.Topic) error {
    item, err := dynamodbattribute.MarshalMap(topic)
    if err != nil {
        return err
    }
    _, err = a.svc.PutItem(&dynamodb.PutItemInput{
        Item: item,
        TableName: aws.String(TOPICS_TABLE),
    })
    return err
}

func (a *DynamoDBAdapter) TopicCreateP2P(initiator, invited *t.Subscription) error {
    
    // Don't care if the initiator changes own subscription
    initiator.Id = initiator.Topic + ":" + initiator.User
    item, err := dynamodbattribute.MarshalMap(initiator)
    if err != nil {
        return err
    }
    _, err = a.svc.PutItem(&dynamodb.PutItemInput{
        Item: item,
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
        Item: item,
        TableName: aws.String(SUBSCRIPTIONS_TABLE),
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
    kv, err := dynamodbattribute.MarshalMap(TopicKey{topic})
    if err != nil {
        return nil, err
    }
    result, err := a.svc.GetItem(&dynamodb.GetItemInput{
        Key: kv,
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
    // fetch all subscriptions owned by user
    eav, err := dynamodbattribute.MarshalMap(map[string]interface{}{
        ":User": uid.String(),
        ":MeTopic": "usr" + uid.String(),
        ":FndTopic": "fnd" + uid.String(),
    })
    if err != nil {
        return nil, err
    }
    input := &dynamodb.QueryInput{
        ExpressionAttributeNames: map[string]*string{
            "#User": aws.String("User"),
            "#Topic": aws.String("Topic"),
        },
        ExpressionAttributeValues: eav,
        KeyConditionExpression: aws.String("#User = :User"),
        FilterExpression: aws.String("#Topic <> :MeTopic and #Topic <> :FndTopic"), // skip over `me` & `fnd` topics
        IndexName: aws.String("UserUpdatedAt"),
        TableName: aws.String(SUBSCRIPTIONS_TABLE),
        //Limit: aws.Int64(int64(MAX_RESULTS)), // ini nggak make sense ya sebenarnya kalau cuma 100?
    }
    if !keepDeleted {
        input.FilterExpression = aws.String("DeletedAt <> NOT_NULL")
    }
    result, err := a.svc.Query(input)
    if err != nil {
        return nil, err
    }
    var items []map[string]*dynamodb.AttributeValue
    items = append(items, result.Items...)
    for len(result.LastEvaluatedKey) > 0 {
        input.ExclusiveStartKey = result.LastEvaluatedKey
        result, err := a.svc.Query(input)
        if err != nil {
            return nil, err
        }
        items = append(items, result.Items...)
    }
    
    var subs []t.Subscription
    if err = dynamodbattribute.UnmarshalListOfMaps(items, &subs); err != nil {
        return nil, err
    }
    
    // parse subscriptions for getting details of topic & user data
    join := make(map[string]*t.Subscription)
    var tkv []map[string]*dynamodb.AttributeValue
    var ukv []map[string]*dynamodb.AttributeValue
    for i := 0; i < len(subs); i++ {
        sub := &subs[i]
        tcat := t.GetTopicCat(sub.Topic)
        
        // 'me' or 'fnd' subscription, skip
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
            ukv = append(ukv, uel)
        }
        tel, err := dynamodbattribute.MarshalMap(TopicKey{sub.Topic})
        if err != nil {
            return nil, err
        }
        tkv = append(tkv, tel)
        join[sub.Topic] = sub
    }
    // fetch topics data
    if len(tkv) > 0 {
        resTopics, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{
            RequestItems: map[string]*dynamodb.KeysAndAttributes{ TOPICS_TABLE: { Keys: tkv } },
        })
        if err != nil {
            return nil, err
        }
        var topics []t.Topic
        if err = dynamodbattribute.UnmarshalListOfMaps(resTopics.Responses[TOPICS_TABLE], &topics); err != nil {
            return nil, err
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
    }
    // fetch users data
    if len(ukv) > 0 {
        resUsers, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{
            RequestItems: map[string]*dynamodb.KeysAndAttributes{ USERS_TABLE: { Keys: ukv } },
        })
        if err != nil {
            return nil, err
        }
        var users []t.User
        if err = dynamodbattribute.UnmarshalListOfMaps(resUsers.Responses[USERS_TABLE], &users); err != nil {
            return nil, err
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
    }
    return subs, nil
}

func (a *DynamoDBAdapter) UsersForTopic(topic string, keepDeleted bool) ([]t.Subscription, error) {
    // get all subscriptions by topic
    eav, err := dynamodbattribute.MarshalMap(map[string]string{
        ":Topic": topic,
    })
    if err != nil {
        return nil, err
    }
    input := &dynamodb.QueryInput{
        ExpressionAttributeValues: eav,
        IndexName: aws.String("Topic"),
        TableName: aws.String(SUBSCRIPTIONS_TABLE),
        KeyConditionExpression: aws.String("Topic = :Topic"),
        //Limit: aws.Int64(int64(MAX_RESULTS)), // ini juga nggak make sense kalau di limit sebenarnya, kecuali ada confignya
    }
    if !keepDeleted {
        input.FilterExpression = aws.String("DeletedAt <> NOT_NULL")
    }
    result, err := a.svc.Query(input)
    if err != nil {
        return nil, err
    }
    var items []map[string]*dynamodb.AttributeValue
    items = append(items, result.Items...)
    for len(result.LastEvaluatedKey) != 0 {
        input.ExclusiveStartKey = result.LastEvaluatedKey
        result, err = a.svc.Query(input)
        if err != nil {
            return nil, err
        }
        items = append(items, result.Items...)
    }
    
    // parse subscriptions
    var subs []t.Subscription
    if err = dynamodbattribute.UnmarshalListOfMaps(items, &subs); err != nil {
        return nil, err
    }
    join := make(map[string]*t.Subscription)
    var kv []map[string]*dynamodb.AttributeValue
    for i := 0; i < len(subs); i++{
        join[subs[i].User] = &subs[i]
        el, err := dynamodbattribute.MarshalMap(UserKey{subs[i].User})
        if err != nil {
            return nil, err
        }
        kv = append(kv, el)
    }
    
    if len(kv) > 0 {
        // fetch public value of user
        resUsers, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{
            RequestItems: map[string]*dynamodb.KeysAndAttributes{USERS_TABLE: { Keys: kv }},
        })
        if err != nil {
            return nil, err
        }
        var usrs []t.User
        if err = dynamodbattribute.UnmarshalListOfMaps(resUsers.Responses[USERS_TABLE], &usrs); err != nil {
            return nil, err
        }
        for _, usr := range usrs {
            if sub, ok := join[usr.Id]; ok {
                sub.ObjHeader.MergeTimes(&usr.ObjHeader)
                sub.SetPublic(usr.Public)
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
        Key: kv,
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
        ExpressionAttributeNames: ean,
        ExpressionAttributeValues: eav,
        UpdateExpression: ue,
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
        Key: kv,
        TableName: aws.String(TOPICS_TABLE),
        ExpressionAttributeNames: ean,
        ExpressionAttributeValues: eav,
        UpdateExpression: ue,
    })
    return err
}

func (a *DynamoDBAdapter) SubscriptionGet(topic string, user t.Uid) (*t.Subscription, error) {
    kv, err := dynamodbattribute.MarshalMap(SubscriptionKey{topic + ":" + user.String()})
    if err != nil {
        return nil, err
    }
    result, err := a.svc.GetItem(&dynamodb.GetItemInput{
        Key: kv,
        TableName: aws.String(SUBSCRIPTIONS_TABLE),
    })
    if err != nil || len(result.Item) == 0 {
        return nil, err
    }
    var sub t.Subscription
    if err = dynamodbattribute.UnmarshalMap(result.Item, &sub); err != nil {
        return nil, err
    }
    return &sub, nil
}

func (a *DynamoDBAdapter) SubsForUser(forUser t.Uid, keepDeleted bool) ([]t.Subscription, error) {
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
        KeyConditionExpression: aws.String("#User = :User"),
        IndexName: aws.String("UserUpdatedAt"),
        TableName: aws.String(SUBSCRIPTIONS_TABLE),
        //Limit: aws.Int64(int64(MAX_RESULTS)),
    }
    if !keepDeleted {
        input.FilterExpression = aws.String("DeletedAt <> NOT_NULL")
    }
    result, err := a.svc.Query(input)
    if err != nil {
        return nil, err
    }

    var items []map[string]*dynamodb.AttributeValue
    items = append(items, result.Items...)
    for len(result.LastEvaluatedKey) > 0 {
        input.ExclusiveStartKey = result.LastEvaluatedKey
        result, err = a.svc.Query(input)
        if err != nil {
            return nil, err
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
    eav, err := dynamodbattribute.MarshalMap(map[string]string{
        ":Topic": topic,
    })
    if err != nil {
        return nil, err
    }
    
    input := &dynamodb.QueryInput{
        ExpressionAttributeValues: eav,
        KeyConditionExpression: aws.String("Topic = :Topic"),
        IndexName: aws.String("Topic"),
        TableName: aws.String(SUBSCRIPTIONS_TABLE),
        //Limit: aws.Int64(int64(MAX_RESULTS)),
    }
    if !keepDeleted {
        input.FilterExpression = aws.String("DeletedAt <> NOT_NULL")
    }
    result, err := a.svc.Query(input)
    if err != nil {
        return nil, err
    }
    var items []map[string]*dynamodb.AttributeValue
    items = append(items, result.Items...)
    for len(result.LastEvaluatedKey) > 0 {
        input.ExclusiveStartKey = result.LastEvaluatedKey
        result, err = a.svc.Query(input)
        if err != nil {
            return nil, err
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
        Key: kv,
        TableName: aws.String(SUBSCRIPTIONS_TABLE),
        ExpressionAttributeNames: ean,
        ExpressionAttributeValues: eav,
        UpdateExpression: ue,
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
    eav, err := dynamodbattribute.MarshalMap(map[string]interface{}{
        ":UpdatedAt": now,
        ":DeletedAt": now,
    })
    if err != nil {
        return err
    }
    _, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
        ExpressionAttributeValues: eav,
        Key: kv,
        TableName: aws.String(SUBSCRIPTIONS_TABLE),
        UpdateExpression: aws.String("set UpdatedAt = :UpdatedAt, DeletedAt = :DeletedAt"),
    })
    return err
}

func (a *DynamoDBAdapter) SubsDelForTopic(topic string) error {
    
    // get subscription ids
    eav, err := dynamodbattribute.MarshalMap(map[string]string{
        ":Topic": topic,
    })
    if err != nil {
        return err
    }
    
    input := &dynamodb.QueryInput{
        ExpressionAttributeNames: map[string]*string{
            "#User": aws.String("User"),
        },
        ExpressionAttributeValues: eav,
        KeyConditionExpression: aws.String("Topic = :Topic"),
        IndexName: aws.String("Topic"),
        TableName: aws.String(SUBSCRIPTIONS_TABLE),
        ProjectionExpression: aws.String("#User"),
    }
    result, err := a.svc.Query(input)
    if err != nil {
        return err
    }
    var items []map[string]*dynamodb.AttributeValue
    items = append(items, result.Items...)
    
    for len(result.LastEvaluatedKey) != 0 {
        input.ExclusiveStartKey = result.LastEvaluatedKey
        result, err = a.svc.Query(input)
        if err != nil {
            return err
        }
        items = append(items, result.Items...)
    }
    
    // delete each subscriptions
    if len(items) > 0 {
        type Record struct {
            User string
        }
        var records []Record
        if err = dynamodbattribute.UnmarshalListOfMaps(items, &records); err != nil {
            return err
        }
        
        // maybe we should use channel to process the records simultaneuosly?
        for _, record := range records {
            if err = a.SubsDelete(topic, t.ParseUid(record.User)); err != nil {
                return err
            }
        }
    }
    return nil
}

func (a *DynamoDBAdapter) FindSubs(uid t.Uid, query []interface{}) ([]t.Subscription, error) {
    
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
    if len(tkvs) > MAX_RESULTS {
        tkvs = tkvs[:MAX_RESULTS] // limit tags
    }
    
    result, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{
        RequestItems: map[string]*dynamodb.KeysAndAttributes{ TAGUNIQUE_TABLE: { Keys: tkvs } },
    })
    if err != nil {
        return nil, err
    }
    
    type Record struct {
        Tag string      `json:"Id"`
        UserId string   `json:"Source"`
    }
    var records []Record
    if err = dynamodbattribute.UnmarshalListOfMaps(result.Responses[TAGUNIQUE_TABLE], &records); err != nil {
        return nil, err
    }
    
    // fetch user id from user for each user id
    var ukvs []map[string]*dynamodb.AttributeValue
    userTagMap := make(map[string]string)
    for _, record := range records {
        // ensure uniqueness of user id in result
        if !uniqueIdx[record.UserId] {
            kv, err := dynamodbattribute.MarshalMap(UserKey{record.UserId})
            if err != nil {
                return nil, err
            }
            ukvs = append(ukvs, kv)
            userTagMap[record.UserId] = record.Tag
            uniqueIdx[record.UserId] = true
        }
    }
    if len(ukvs) > MAX_RESULTS {
        ukvs = ukvs[:MAX_RESULTS] // limit users
    }
    resUsers, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{
        RequestItems: map[string]*dynamodb.KeysAndAttributes{ USERS_TABLE: { Keys: ukvs } },
    })
    if err != nil {
        return nil, err
    }
    
    // parse result
    var users []t.User
    if err = dynamodbattribute.UnmarshalListOfMaps(resUsers.Responses[USERS_TABLE], &users); err != nil {
        return nil, err
    }
    var subs []t.Subscription
    for _, user := range users {
        if user.Id == uid.String() {
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
    msg.SetUid(store.GetUid())
    item, err := dynamodbattribute.MarshalMap(msg)
    if err != nil {
        return err
    }
    
    if *item["DeletedFor"].NULL == true {
        item["DeletedFor"] = &dynamodb.AttributeValue{L: []*dynamodb.AttributeValue{}}
    }
    
    // set expire duration
    expireDurationInSeconds := EXPIRE_DURATION_MESSAGE_ME
    switch(t.GetTopicCat(msg.Topic)) {
    case t.TopicCat_P2P:
        expireDurationInSeconds = EXPIRE_DURATION_MESSAGE_P2P
    case t.TopicCat_Grp:
        expireDurationInSeconds = EXPIRE_DURATION_MESSAGE_GROUP
    }
    expireTimeUnix := time.Now().UTC().Add(time.Duration(expireDurationInSeconds) * time.Second).Unix()
    item["ExpireTime"] = &dynamodb.AttributeValue{N: aws.String(fmt.Sprintf("%d", expireTimeUnix))}
    
    _, err = a.svc.PutItem(&dynamodb.PutItemInput{
        Item: item,
        TableName: aws.String(MESSAGES_TABLE),
    })
    return err
}

// ini nanti pattern fetch message perlu dijelaskan ke k.dimas sm k.yacob
// ini perlu di test dgn payload message yg banyak
func (a *DynamoDBAdapter) MessageGetAll(topic string, forUser t.Uid, opts *t.BrowseOpt) ([]t.Message, error) {
    
    since := 0
    before := math.MaxInt32
    limit := uint(MAX_MESSAGES_RETRIEVED)
    
    if opts != nil {
        if opts.Since > 0 {
            since = opts.Since
        }
        if opts.Before > 0 {
            before = opts.Before
        }
        if opts.Limit > 0 && opts.Limit < limit {
            limit = opts.Limit
        }
    }
    
    eav, err := dynamodbattribute.MarshalMap(map[string]interface{}{
        ":Topic": topic,
        ":Since": since,
        ":Before": before,
    })
    if err != nil {
        return nil, err
    }
    
    result, err := a.svc.Query(&dynamodb.QueryInput{
        ExpressionAttributeValues: eav,
        KeyConditionExpression: aws.String("Topic = :Topic and SeqId between :Since and :Before"),
        TableName: aws.String(MESSAGES_TABLE),
        Limit: aws.Int64(int64(limit)),
        ScanIndexForward: aws.Bool(false),
    })
    if err != nil {
        return nil, err
    }
    var items []map[string]*dynamodb.AttributeValue
    items = append(items, result.Items...)
    
    for len(result.LastEvaluatedKey) != 0 {
        result, err := a.svc.Query(&dynamodb.QueryInput{
            ExpressionAttributeValues: eav,
            KeyConditionExpression: aws.String("Topic = :Topic and SeqId between :Since and :Before"),
            TableName: aws.String(MESSAGES_TABLE),
            Limit: aws.Int64(int64(limit)),
            ExclusiveStartKey: result.LastEvaluatedKey,
            ScanIndexForward: aws.Bool(false),
        })
        if err != nil {
            return nil, err
        }
        items = append(items, result.Items...)
    }
        
    var msgs []t.Message
    if err = dynamodbattribute.UnmarshalListOfMaps(items, &msgs); err != nil {
        return nil, err
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
            Key: kv,
            TableName: aws.String(USERS_TABLE),
            UpdateExpression: ue,
            ConditionExpression: ce,
        })
        if err != nil {
            if aerr, ok := err.(awserr.Error); ok && (aerr.Code() == dynamodb.ErrCodeConditionalCheckFailedException){
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
            Key: kv,
            TableName: aws.String(TOPICS_TABLE),
            UpdateExpression: ue,
            ConditionExpression: ce,
        })
        if err != nil {
            if aerr, ok := err.(awserr.Error); ok && (aerr.Code() == dynamodb.ErrCodeConditionalCheckFailedException){
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
                Key: kv,
                TableName: aws.String(SUBSCRIPTIONS_TABLE),
                UpdateExpression: ue,
                ConditionExpression: ce,
            })
            if err != nil {
                if aerr, ok := err.(awserr.Error); ok && (aerr.Code() == dynamodb.ErrCodeConditionalCheckFailedException){
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
        go func(seqId int){
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
                        User: forUser.String(),
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
                Key: kv,
                TableName: aws.String(MESSAGES_TABLE),
                UpdateExpression: ue,
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
    ean := map[string]*string { "#device": aws.String(hash) }
    eav, err := dynamodbattribute.MarshalMap(map[string]*t.DeviceDef{ ":device": dev })
    if err != nil {
        return err
    }
    ue := aws.String("SET Devices.#device = :device")
    _, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
        ExpressionAttributeNames: ean,
        ExpressionAttributeValues: eav,
        Key: kv,
        TableName: aws.String(USERS_TABLE),
        UpdateExpression: ue,
    })
    return err
}

func (a *DynamoDBAdapter) DeviceGetAll(uids ...t.Uid) (map[t.Uid][]t.DeviceDef, int, error) {
    // get devices for each uid
    var kvs []map[string]*dynamodb.AttributeValue
    for _, uid := range uids {
        el, err := dynamodbattribute.MarshalMap(UserKey{uid.String()})
        if err != nil {
            kvs = append(kvs, el)
        }
    }
    resUsers, err := a.svc.BatchGetItem(&dynamodb.BatchGetItemInput{
        RequestItems: map[string]*dynamodb.KeysAndAttributes{ USERS_TABLE: { Keys: kvs, ProjectionExpression: aws.String("Id, Devices") } },
    })
    if err != nil {
        return nil, 0, err
    }
    type Record struct {
        Id string
        Devices map[string]*t.DeviceDef
    }
    var records []Record
    if err = dynamodbattribute.UnmarshalListOfMaps(resUsers.Responses[USERS_TABLE], &records); err != nil {
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
    ean := map[string]*string { "#device": aws.String(hash) }
    ue := aws.String("REMOVE Devices.#device")
    _, err = a.svc.UpdateItem(&dynamodb.UpdateItemInput{
        ExpressionAttributeNames: ean,
        Key: kv,
        TableName: aws.String(USERS_TABLE),
        UpdateExpression: ue,
    })
    return err
}

func init() {
    store.Register("dynamodb", &DynamoDBAdapter{})
}