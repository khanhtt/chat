# DynamoDB Database Schema

## Table `TinodeUsers`
Stores user accounts

### Fields:
* `Id` user id, primary key
* `CreatedAt` timestamp when the user was created
* `UpdatedAt` timestamp when user metadata was updated
* `DeletedAt` currently unused
* `Access` user's default access level for peer-to-peer topics
    * `Auth`, `Anon` default permissions for authenticated and anonymous users
* `Public` application-defined data
* `State` currently unused
* `LastSeen` timestamp when the user was last online
* `UserAgent` client User-Agent used when last online
* `SeqId` last message id on user's 'me' topics
* `ClearId` maximum message id that's been cleared (deleted)
* `Tags` unique strings for user discovery
* `Devices` client devices for push notifications
    * `DeviceId` device registration ID
    * `Platform` device platform (iOS, Android, Web)
    * `LastSeen` last logged in
    * `Lang` device language, ISO code
 
### Indexes:
* `Primary Key`: {PartitionKey: `Id`}

### Sample:
```js
{
  "Access": {
    "Anon": 0,
    "Auth": 31
  },
  "ClearId": 0,
  "CreatedAt": "2017-09-25T09:26:11.469Z",
  "DeletedAt": null,
  "Devices": {},
  "Id": "KKyCVA956m8",
  "LastSeen": "2017-10-04T13:42:29.612Z",
  "Public": {
    "fn": "tndbomb_4",
    "photo": {
      "data": "/9j/4A...B//9k=",
      "type": "jpg"
    }
  },
  "SeqId": 4,
  "State": 0,
  "Tags": [
    "email:tndbomb_4@example.com"
  ],
  "UpdatedAt": "2017-10-04T13:41:37.221Z",
  "UserAgent": "TinodeWeb/0.13 (MacIntel) tinodejs/0.13"
}
```

## Table `TinodeAuth`
Stores authentication secrets

### Fields:
* `userid` ID of the user who owns the record
* `unique` unique string which identifies this record, primary key; defined as "_authentication scheme_':'_some unique value per scheme_"
* `secret` shared secret, for instance bcrypt of password
* `authLvl` authentication level
* `expires` timestamp when the records expires

### Indexes:
* `Primary Key`: {PartitionKey: `unique`}
* `userid`: {PartitionKey: `userid`}

### Sample:
```js
{
  "authLvl": 20,
  "expires": "0001-01-01T00:00:00Z",
  "secret": "JDJhJDEwJEFOenlnZU1ZOXMwLkZUTFVRUHVYZS56TFFuWEhDb28yMFhEaEFTRHZsMlNDbDJIV0VMbzZh",
  "unique": "basic:tndbomb_5",
  "userid": "9Si0WE3N_0Q"
}
```

## Table `TinodeTagUnique`
Indexed user tags, to ensure tag uniqueness & user lookup by tags

### Fields:
`Id` unique tag, primary keys
`Source` ID of the user who owns the tag

### Indexes:
* `Primary Key`: {PartitionKey: `Id`}
* `Source`: {PartitionKey: `Source`}

### Sample:
```js
{
  "Id": "email:tndbomb_50@example.com",
  "Source": "vnupzyrripA"
}
```

## Table `TinodeTopics`
The table stores topics.

### Fields:
* `Id` name of the topic, primary key
* `CreatedAt` topic creation time
* `UpdatedAt` timestamp of the last change to topic metadata
* `DeletedAt` currently unused
* `Access` stores topic's default access permissions
    * `Auth`, `Anon` permissions for authenticated and anonymous users respectively
* `Public` application-defined data
* `State` currently unused
* `SeqId` id of the last message
* `ClearId` id of the message last cleared (deleted)
* `UseBt` currently unused

### Indexes:
* `Primary Key`: {PartitionKey: `Id`} 

### Sample:
```js
{
  "Access": {
    "Anon": 0,
    "Auth": 0
  },
  "ClearId": 0,
  "CreatedAt": "2017-09-26T14:08:26.104Z",
  "DeletedAt": null,
  "Id": "p2pAlPRDF12iirthS2WpDz1eg",
  "Public": null,
  "SeqId": 16,
  "State": 0,
  "UpdatedAt": "2017-09-26T14:08:26.104Z",
  "UseBt": false
}
```

## Table `TinodeSubscriptions`
The table stores relationships between users and topics.

### Fields:
* `Id` used for object retrieval
* `CreatedAt` timestamp when the user was created
* `UpdatedAt` timestamp when user metadata was updated
* `DeletedAt` currently unused
* `ReadSeqId` id of the message last read by the user
* `RecvSeqId` id of the message last received by user device
* `ClearedId` user soft-deleted messages with id lower or equal to this id
* `Topic` name of the topic subscribed to
* `User` subscriber's user ID
* `ModeWant` access mode that user wants when accessing the topic
* `ModeGiven` access mode granted to user by the topic
* `Private` application-defined data, accessible by the user only

### Indexes:
* `Primary Key`: {PartitionKey: `Id`} 
* `UserUpdatedAt`: {PartitionKey: `User`, RangeKey: `UpdatedAt`}
* `Topic`: {PartitionKey: `Topic`}

### Sample:
```js
{
  "ClearId": 0,
  "CreatedAt": "2017-09-26T14:04:16.792Z",
  "DeletedAt": null,
  "Id": "p2pGzLWrkc4ECYv8wKBpOKkkg:L_MCgaTipJI",
  "ModeGiven": 31,
  "ModeWant": 0,
  "Private": null,
  "ReadSeqId": 0,
  "RecvSeqId": 0,
  "State": 0,
  "Topic": "p2pGzLWrkc4ECYv8wKBpOKkkg",
  "UpdatedAt": "2017-09-26T14:04:16.792Z",
  "User": "L_MCgaTipJI"
}
```

## Table `TinodeMessages`
The table stores `{data}` messages

### Fields:
* `Id` currently unused, primary key
* `CreatedAt` timestamp when the message was created
* `UpdatedAt` initially equal to CreatedAt, for deleted messages equal to DeletedAt
* `DeletedAt` timestamp when the message was deleted for all users
* `DeletedFor` IDs of the users who have soft-deleted the message
* `From` ID of the user who generated this message
* `Topic` which received this message
* `SeqId` id of the message
* `Head` message headers
* `Content` application-defined message payload
* `ExpireTime` unix timestamp for marking expire time of message, if it already passed then the record would be automatically deleted 

### Indexes:
* `Primary Key`: {PartitonKey: `Topic`, RangeKey: `SeqId`} 

### Sample:
```js
{
  "Content": "Hello this is message from tinode bombardier! :D",
  "CreatedAt": "2017-09-26T14:05:15.361Z",
  "DeletedAt": null,
  "DeletedFor": [],
  "ExpireTime": 1537970847,
  "From": "FcjMAj2Q8D8",
  "Head": null,
  "Id": "QFzKAEbEyrw",
  "SeqId": 5,
  "Topic": "p2pAlPRDF12iioVyMwCPZDwPw",
  "UpdatedAt": "2017-09-26T14:05:15.361Z"
}
```