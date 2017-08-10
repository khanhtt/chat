# Tinode Protocol Simple Documentation

Hello, Guys!

This document contains simple documentation for Tinode Protocol. As you’ve already know, Tinode project is still in its early stage + I'm still exploring it, so please expect there will be a lot of change to this document in the future.

This documentation is made purely based on my understanding on Tinode. If you found some issues or want to contribute to this doc, feel free to comment or make pull request.

Thanks.

## Basic Concept
Tinode follow loosely publish & subscribe architecture. So it is a little bit different than normal http request & response architecture. For example in Tinode, it is common to get multiple packet as a response just from a single request packet.

Notice following example request for getting message history from P2P topic we are currently subscribing:
```
{
  "get": {
    "id": "get p2p history messages with new 2",
    "topic": "usroVhYlDImeqk",
    "what": "data"
  }
}
```
The response of this request would be:
```
{
  "data": {
    "topic": "usroVhYlDImeqk",
    "from": "usroVhYlDImeqk",
    "ts": "2017-08-05T23:40:12.426Z",
    "seq": 1,
    "content": "Hello"
  }
}

{
  "data": {
    "topic": "usroVhYlDImeqk",
    "from": "usroVhYlDImeqk",
    "ts": "2017-08-06T00:09:30.265Z",
    "seq": 2,
    "content": "Hello, new!"
  }
}

{
  "data": {
    "topic": "usroVhYlDImeqk",
    "from": "usroVhYlDImeqk",
    "ts": "2017-08-06T00:09:41.552Z",
    "seq": 3,
    "content": "This is new2"
  }
}

{
  "data": {
    "topic": "usroVhYlDImeqk",
    "from": "usroVhYlDImeqk",
    "ts": "2017-08-06T00:09:46.704Z",
    "seq": 4,
    "content": "do you remember?"
  }
}

{
  "ctrl": {
    "id": "get p2p history messages with new 2",
    "topic": "usroVhYlDImeqk",
    "code": 200,
    "text": "ok",
    "ts": "2017-08-06T00:09:59.847Z"
  }
}
```

There are some other differences we need to understand in Tinode. But I will just put each of them on the corresponding sections.

## Initiating Connection

In Tinode, after we successfully connecting socket connection to the server, the first thing we need to do is sending `{hi}` packet. After the packet successfully accepted by the server, we could then proceed to:

- create new account by sending `{acc}` packet; or
- login with existing account by sending `{login}` packet

**Example request:**
```
{  
   "hi":{  
      "id":"initiating connection",
      "ver":"0.13",
      "ua":"TinodeWeb/0.13 (MacIntel) tinodejs/0.13"
   }
}
```

**Success response:**
```
{
  "ctrl": {
    "id": "initiating connection",
    "params": {
      "build": "",
      "ver": "0.13"
    },
    "code": 201,
    "text": "created",
    "ts": "2017-08-05T15:37:08.547Z"
  }
}
```
**Error response:**
```
{
  "ctrl": {
    "id": "initiating connection unsupported version",
    "code": 505,
    "text": "version not supported",
    "ts": "2017-08-06T00:47:10.194Z"
  }
}
```

**References:**

- https://github.com/tinode/chat/blob/devel/API.md#hi

## Create New Account Credentials

To create new account, we use `{acc}` packet. Notice that the value of `acc.user` need to be set to `"new"`.

**Example Request:**
```
{  
   "acc":{  
      "id":"create new user",
      "user":"new",
      "scheme":"basic",
      "secret":"bmV3MTpuZXcx",
      "tags":[  
         "email:new1@example.com"
      ],
      "desc":{  
         "public":{  
            "fn":"new1"
         }
      }
   }
}
```
There are 3 other values need to be noticed: `acc.secret`, `acc.tags`, & `acc.desc.public`.

`acc.secret` should contains credential information in form of `username:password` which already encoded by `base64` function. This is the format we are using since we set `acc.scheme` to `"basic"`. In above example, if we decode	`acc.secret` value, it will return `new1:new1`.

`acc.tags` should contains array of string which will make this user discoverable by other user. If this value omitted, this user won't be able to be discovered by other user. But still it could interact with others.

`acc.desc.public` should contains information which will be interpreted by client app. In this case, since I use example web app, I set the value to `{"fn":"new1"}` which will be interpreted by example web app as display name of the user. So later in our own app we could input any information that we want.

Notice that after the account created, the information we put on `acc.desc` will be transferred to `me` topic of this account. Thus if we want to change this information later, we need to change it from `me` topic. For other 2 values, we still need to change it via `{acc}` packet.

**Success Response:**
```
{
  "ctrl": {
    "id": "create new user",
    "params": {
      "desc": {
        "created": "2017-08-05T16:15:40.869Z",
        "updated": "2017-08-05T16:15:40.869Z",
        "defacs": {
          "auth": "JRWP",
          "anon": "N"
        },
        "public": {
          "fn": "new1"
        }
      },
      "user": "usr3BbzhEyaiPU"
    },
    "code": 201,
    "text": "created",
    "ts": "2017-08-05T16:15:40.868Z"
  }
}
```
**Error Response:**
```
{
  "ctrl": {
    "id": "create new user",
    "code": 409,
    "text": "duplicate credential",
    "ts": "2017-08-05T16:16:43.082Z"
  }
}
```
```
{
  "ctrl": {
    "id": "create new user",
    "code": 409,
    "text": "already authenticated",
    "ts": "2017-08-05T16:29:14.854Z"
  }
}
```

There is also one optional parameter called `login`. We could set this parameter to `true` if we want our newly created user to be logged in directly.

**Example request:**
```
{
  "acc": {
    "id": "create new user",
    "user": "new",
    "scheme": "basic",
    "secret": "bmV3MjpuZXcy",
    "login": true,
    "tags": [
      "email:new2@example.com"
    ],
    "desc": {
      "public": {
        "fn": "new2"
      }
    }
  }
}
```
**Success response:**
```
{
  "ctrl": {
    "id": "create new user",
    "params": {
      "authlvl": "auth",
      "desc": {
        "created": "2017-08-05T16:27:47.738Z",
        "updated": "2017-08-05T16:27:47.738Z",
        "defacs": {
          "auth": "JRWP",
          "anon": "N"
        },
        "public": {
          "fn": "new2"
        }
      },
      "expires": "2017-08-05T23:27:47.912749621+07:00",
      "token": "oVhYlDImeqkD8oVZFAAAAM+jp2qKGhJ5tX3g5Y++O10MADe/SwJCf6BUpZwDlaAt",
      "user": "usroVhYlDImeqk"
    },
    "code": 201,
    "text": "created",
    "ts": "2017-08-05T16:27:47.737Z"
  }
}
```
**Error response:**
```
{
  "ctrl": {
    "id": "create new user",
    "code": 409,
    "text": "duplicate credential",
    "ts": "2017-08-05T16:16:43.082Z"
  }
}
```
```
{
  "ctrl": {
    "id": "create new user",
    "code": 409,
    "text": "already authenticated",
    "ts": "2017-08-05T16:29:14.854Z"
  }
}
```
**References:**

- https://github.com/tinode/chat/blob/devel/API.md#acc

## Update Existing Account Credentials
 To update existing account credentials, we use `{acc}` packet with omitting `acc.user` field. The credentials possible to be updated with this packet are `acc.secret` & `acc.tags`.

Unfortunately this feature has yet to be supported.

**References:**

- https://github.com/tinode/chat/blob/devel/server/session.go#L601-L611

## Login
In Tinode, when we are authenticating the user, technically we are not authenticating the user itself, but rather authenticating current session which the user using to connect to server.

So what happens when websocket session suddenly destroyed? Such as when there is a connection problem whether on client or server. Do we need to authenticate again?

The answer to above question is yes, we need to authenticate our session again. But since it would be troublesome if we ask user to input username & password again, beside `basic` authentication scheme, Tinode also offers `token` authentication scheme. The token used for `token` authentication is obtained from the previous `basic` authentication.

By design, it is possible for one user to have multiple active session at the same time. Thus user could login from multiple devices at the same time.

**Example `basic` request:**
```
{
  "login": {
    "id": "user login",
    "scheme": "basic",
    "secret": "bmV3MTpuZXcx"
  }
}
```

**Example `token` request:**
```
{
  "login": {
    "id": "user login",
    "scheme": "token",
    "secret": "3BbzhEyaiPUle5tZFAABAKICLHJsZQq0D2WTquYowk/yjpZcdzaQSBrmq0WckdBJ"
  }
}
```

**Success response:**
```
{
  "ctrl": {
    "id": "user login",
    "params": {
      "expires": "2017-08-22T00:30:29Z",
      "token": "3BbzhEyaiPUle5tZFAABAKICLHJsZQq0D2WTquYowk/yjpZcdzaQSBrmq0WckdBJ",
      "user": "usr3BbzhEyaiPU"
    },
    "code": 200,
    "text": "OK",
    "ts": "2017-08-08T07:17:22.932Z"
  }
}
```

**Error responses:**
```
{
  "ctrl": {
    "id": "user login",
    "code": 401,
    "text": "authentication failed",
    "ts": "2017-08-06T02:59:26.691Z"
  }
}
```
```
{
  "ctrl": {
    "id": "user login",
    "code": 409,
    "text": "already authenticated",
    "ts": "2017-08-06T02:55:16.284Z"
  }
}
```

**References:**

- https://github.com/tinode/chat/blob/devel/API.md#login

## Make Online Presence

### Subscribe `me` Topic

When user login, it doesn't automatically come online. It needs to subscribe to its `me` topic first. When user already subscribed, its presence would be published to all other users which currently subscribed to this user's `me` topic (have `P2P` connection with this user).

When other user interact with this user's `me` topic, such as sending message to this user, create new subscription  to this user, & invite this user to a group topic, then this user would also be notified. We could use `{sub}` packet to make current session subscribe to `me` topic.

**Example request:**
```
{
  "sub": {
    "id": "make online presence",
    "topic": "me"
  }
}
```

**Success response:**
```
{
  "ctrl": {
    "id": "make online presence",
    "topic": "me",
    "params": {
      "acs": {
        "want": "JRPD",
        "given": "JRPD",
        "mode": "JRPD"
      }
    },
    "code": 200,
    "text": "ok",
    "ts": "2017-08-06T06:42:31.78Z"
  }
}
```

**Error response:**
```
{
  "ctrl": {
    "id": "make online presence",
    "code": 304,
    "text": "already subscribed",
    "ts": "2017-08-10T09:48:42.012Z"
  }
}
```

Notice that `sub` packet doesn't only used for subscribing to `me` topic, but also for any other topics: `p2p`, `group`, & `fnd`.

### Get `me` Profile

To get `me` profile information (public information, private information, & default acccess for `P2P` topic), we use `{get}` packet with `get.what` value set to `"desc"`

**Example request:**
```
{
  "get": {
    "id": "get me profile",
    "topic": "me",
    "what": "desc"
  }
}
```

**Success response:**
```
{
  "meta": {
    "id": "get me profile",
    "topic": "me",
    "ts": "2017-08-10T11:38:12.567Z",
    "desc": {
      "created": "2017-08-05T16:15:40.869Z",
      "updated": "2017-08-09T22:33:32.85Z",
      "defacs": {
        "auth": "JRWPA",
        "anon": "N"
      },
      "seq": 6,
      "public": {
        "fn": "new1"
      }
    }
  }
}

```

### Get `me` Subscribers

To get existing `me` subscribers (a.k.a other users which already have P2P connection with this user), we use `{get}` packet with `get.what` value set to `"sub"`.

**Example request:**
```
{
  "get": {
    "id": "get me subscribers",
    "topic": "me",
    "what": "sub"
  }
}
```

**Success response:**
```
{
  "meta": {
    "id": "get me subscribers",
    "topic": "me",
    "ts": "2017-08-06T09:19:11.328Z",
    "sub": [
      {
        "updated": "2017-08-05T23:56:32.073Z",
        "acs": {
          "want": "JRWP",
          "given": "JRWP",
          "mode": "JRWP"
        },
        "public": {
          "fn": "new2"
        },
        "topic": "usroVhYlDImeqk",
        "seq": 5,
        "seen": {
          "when": "2017-08-06T06:44:11.053Z",
          "ua": "TinodeWeb/0.13 (MacIntel) tinodejs/0.13"
        }
      },
      {
        "updated": "2017-08-05T23:56:32.073Z",
        "acs": {
          "want": "JRWP",
          "given": "JRWP",
          "mode": "JRWP"
        },
        "public": {
          "fn": "new3"
        },
        "topic": "usrolJNm1H2-Z8",
        "seq": 1,
        "seen": {
          "when": "2017-08-06T02:09:44.994Z",
          "ua": "TinodeWeb/0.13 (MacIntel) tinodejs/0.13"
        }
      }
    ]
  }
}
```

### Get `me` notification

To get `me` notification (invitation to join topic & request approval), we use `{get}` packet with `get.what` value set to `"data"`.

**Example request:**
```
{
  "get": {
    "id": "get me profile",
    "topic": "me",
    "what": "data"
  }
}
```

**Success response:**
```
{
  "data": {
    "topic": "me",
    "from": "usroVhYlDImeqk",
    "ts": "2017-08-05T23:40:09.926Z",
    "seq": 1,
    "content": {
      "acs": {
        "given": "JRWP",
        "mode": "JRWP",
        "want": "JRWP"
      },
      "act": "upd",
      "authlvl": "auth",
      "topic": "usroVhYlDImeqk",
      "user": "usr3BbzhEyaiPU"
    }
  }
}

{
  "data": {
    "topic": "me",
    "from": "usrolJNm1H2-Z8",
    "ts": "2017-08-05T23:56:32.14Z",
    "seq": 2,
    "content": {
      "acs": {
        "given": "JRWP",
        "mode": "JRWP",
        "want": "JRWP"
      },
      "act": "upd",
      "authlvl": "auth",
      "topic": "usrolJNm1H2-Z8",
      "user": "usr3BbzhEyaiPU"
    }
  }
}

{
  "data": {
    "topic": "me",
    "from": "usroVhYlDImeqk",
    "ts": "2017-08-08T03:51:56.393Z",
    "seq": 3,
    "content": {
      "acs": {
        "given": "JRWPS",
        "mode": "JRWP",
        "want": "JRWP"
      },
      "act": "inv",
      "info": "Join my group!",
      "topic": "grpwGrK1TJhoeQ",
      "user": "usr3BbzhEyaiPU"
    }
  }
}

{
  "data": {
    "topic": "me",
    "from": "usrWWw5MTFGJWU",
    "ts": "2017-08-09T11:41:45.867Z",
    "seq": 4,
    "content": {
      "acs": {
        "given": "JRWPA",
        "mode": "JRWP",
        "want": "JRWP"
      },
      "act": "upd",
      "authlvl": "auth",
      "topic": "usrWWw5MTFGJWU",
      "user": "usr3BbzhEyaiPU"
    }
  }
}

{
  "data": {
    "topic": "me",
    "from": "usr2SBAaU1Hqrk",
    "ts": "2017-08-09T11:51:30.401Z",
    "seq": 5,
    "content": {
      "acs": {
        "given": "JRWPA",
        "mode": "JRWP",
        "want": "JRWP"
      },
      "act": "upd",
      "authlvl": "auth",
      "topic": "usr2SBAaU1Hqrk",
      "user": "usr3BbzhEyaiPU"
    }
  }
}

{
  "data": {
    "topic": "me",
    "from": "usrV9_cBOsVGyw",
    "ts": "2017-08-09T22:31:13.546Z",
    "seq": 6,
    "content": {
      "acs": {
        "given": "JRWPA",
        "mode": "JRWP",
        "want": "JRWP"
      },
      "act": "upd",
      "authlvl": "auth",
      "topic": "usrV9_cBOsVGyw",
      "user": "usr3BbzhEyaiPU"
    }
  }
}

{
  "ctrl": {
    "id": "get me profile",
    "topic": "me",
    "code": 200,
    "text": "ok",
    "ts": "2017-08-10T11:47:40.319Z"
  }
}
```

### Bulk Command to Subscribe, Get Profile, Get Subscribers, & Get Notification of `me`

Luckily Tinode provide convenient way to do all above actions. We could just do it with following command:
```
{  
   "sub":{  
      "id":"make online presence",
      "topic":"me",
      "get":{  
         "what":"desc sub data"
      }
   }
}
```

If we only want to fetch the `me` info, we could use following command:
```
{
  "get": {
    "id": "get me info",
    "topic": "me",
    "what": "desc sub data"
  }
}
```

The response of these commands are combination between all of actions called.

**References:**

- https://github.com/tinode/chat/blob/devel/API.md#sub
- https://github.com/tinode/chat/blob/devel/API.md#get
- https://github.com/tinode/chat/blob/devel/API.md#topics

### Receiving `me` Notifications

When the user already subscribed to `me` topic, they will be able to receive notifications from the topics which the user already have connection with. The notification itself is sent by server in form of `{pres}` packet.

The notification will be sent by the server on various use cases. Some of the most common use cases are receiving online & message notification from other user (message notification is not the message itself, basically it is `{pres}` packet telling that other user is sending message to this user). For the complete list of use cases please see the references.

**Example packets:**
```
{
  "pres": {
    "topic": "me",
    "src": "usroVhYlDImeqk",
    "what": "on"
  }
}
```

```
{
  "pres": {
    "topic": "me",
    "src": "usroVhYlDImeqk",
    "what": "msg",
    "seq": 30
  }
}
```

**References:**

- https://github.com/tinode/chat/blob/devel/API.md#pres

## Chat With Another User (P2P Topic)

### Discover Another User

If we want to connect with new user, the first thing we need to know is its `id`. Fortunately in Tinode there is a special topic which we could use to discover other user's `id` which is called `fnd` topic. If we want to connect to already known user, please proceed to next section.

So how does this work? First we need to subscribe to `fnd` topic using `{sub}` packet. Then we set the user tags to private value on `fnd` topic by using `set` packet. After `set` packet is acknowledged by the server, then we send `get` packet to retrieve the results. After we process the result, we set `fnd` private value to empty, then we send `leave` packet to leave `fnd` topic.

**Example `sub` request:**
```
{
  "sub": {
    "id": "subscribe to topic fnd",
    "topic": "fnd"
  }
}
```

**Success `sub` response:**
```
{
  "ctrl": {
    "id": "subscribe to topic fnd",
    "topic": "fnd",
    "params": {
      "acs": {
        "want": "JRPD",
        "given": "JRPD",
        "mode": "JRPD"
      }
    },
    "code": 200,
    "text": "ok",
    "ts": "2017-08-06T09:59:44.308Z"
  }
}
```

**Example `set` request:**
```
{
  "set": {
    "id": "set private value topic fnd",
    "topic": "fnd",
    "desc": {
      "private": [
        "email:new2@example.com"
      ]
    }
  }
}
```

**Success `set` response:**
```
{
  "ctrl": {
    "id": "set private value topic fnd",
    "topic": "fnd",
    "code": 200,
    "text": "ok",
    "ts": "2017-08-06T10:03:35.103Z"
  }
}
```

**Example `get` request:**
```
{
  "get": {
    "id": "get search result of topic fnd",
    "topic": "fnd",
    "what": "sub"
  }
}
```

**Success `get` response:**
```
{
  "meta": {
    "id": "get search result of topic fnd",
    "topic": "fnd",
    "ts": "2017-08-06T10:05:02.508Z",
    "sub": [
      {
        "updated": "2017-08-05T16:27:47.738Z",
        "acs": {
          "mode": "JRWP"
        },
        "public": {
          "fn": "new2"
        },
        "private": [
          "email:new2@example.com"
        ],
        "user": "usroVhYlDImeqk"
      }
    ]
  }
}
```
**Example `set` request:**
```
{
  "set": {
    "id": "set private value topic fnd",
    "topic": "fnd",
    "desc": {
      "private": []
    }
  }
}
```

**Success `set` request:**
```
{
  "ctrl": {
    "id": "set private value topic fnd",
    "topic": "fnd",
    "code": 200,
    "text": "ok",
    "ts": "2017-08-06T10:22:23.323Z"
  }
}
```

**Example `leave` request:**
```
{
  "leave": {
    "id": "leave fnd topic",
    "topic": "fnd"
  }
}
```

**Success `leave` response:**
```
{
  "ctrl": {
    "id": "leave fnd topic",
    "topic": "fnd",
    "code": 200,
    "text": "ok",
    "ts": "2017-08-06T10:13:51.276Z"
  }
}
```

Currently there is no single command to do all of these actions.

**References:**

- https://github.com/tinode/chat/blob/devel/API.md#fnd-topic-contacts-discovery
- https://github.com/tinode/chat/blob/devel/API.md#leave

### Subscribing / Activate P2P Topic

After we found the user's `id` we want to connect which located in `meta.sub[0].user` (or `meta.sub[i].user` if we select from `me` subscribers result), we could then pass this value to `sub.topic` in `{sub}` packet.

After we subscribed to `p2p` topic, we will be able to receive message from & send message to our target.

**Example request:**
```
{
  "sub": {
    "id": "subscribe p2p topic with new2",
    "topic": "usroVhYlDImeqk"
  }
}
```

**Success response:**
```
{
  "ctrl": {
    "id": "subscribe p2p topic with new2",
    "topic": "usroVhYlDImeqk",
    "params": {
      "acs": {
        "want": "JRWP",
        "given": "JRWP",
        "mode": "JRWP"
      }
    },
    "code": 200,
    "text": "ok",
    "ts": "2017-08-06T11:57:31.642Z"
  }
}
```

**References:**

- https://github.com/tinode/chat/blob/devel/API.md#sub

### Getting P2P Message History

To get message history, we use `get` packet.

**Example request:**
```
{
  "get": {
    "id": "get p2p history messages with new 2",
    "topic": "usroVhYlDImeqk",
    "what": "data"
  }
}
```

**Success response:**
```
{
  "data": {
    "topic": "usroVhYlDImeqk",
    "from": "usroVhYlDImeqk",
    "ts": "2017-08-05T23:40:12.426Z",
    "seq": 1,
    "content": "Hello"
  }
}

{
  "data": {
    "topic": "usroVhYlDImeqk",
    "from": "usroVhYlDImeqk",
    "ts": "2017-08-06T00:09:30.265Z",
    "seq": 2,
    "content": "Hello, new!"
  }
}

{
  "data": {
    "topic": "usroVhYlDImeqk",
    "from": "usroVhYlDImeqk",
    "ts": "2017-08-06T00:09:41.552Z",
    "seq": 3,
    "content": "This is new2"
  }
}

{
  "data": {
    "topic": "usroVhYlDImeqk",
    "from": "usroVhYlDImeqk",
    "ts": "2017-08-06T00:09:46.704Z",
    "seq": 4,
    "content": "do you remember?"
  }
}

{
  "ctrl": {
    "id": "get p2p history messages with new 2",
    "topic": "usroVhYlDImeqk",
    "code": 200,
    "text": "ok",
    "ts": "2017-08-06T00:09:59.847Z"
  }
}
```

**References:**

- https://github.com/tinode/chat/blob/devel/API.md#get

### Send Message to P2P Topic

We could use `{pub}` packet to send message to `p2p` topic we are already subscribed.

**Example request:**
```
{
  "pub": {
    "id": "send message to new2",
    "topic": "usroVhYlDImeqk",
    "content": "hello this is message from new1"
  }
}
```
**Success response:**
```
{
  "ctrl": {
    "id": "send message to new2",
    "topic": "usroVhYlDImeqk",
    "params": {
      "seq": 6
    },
    "code": 202,
    "text": "accepted",
    "ts": "2017-08-06T12:48:53.226Z"
  }
}

{
  "data": {
    "topic": "usroVhYlDImeqk",
    "from": "usr3BbzhEyaiPU",
    "ts": "2017-08-06T12:48:53.226Z",
    "seq": 6,
    "content": "hello this is message from new1"
  }
}
```
**References:**

- https://github.com/tinode/chat/blob/devel/API.md#pub

### Receiving Message from P2P Topic

We will receive the message sent by other user by `{pub}` packet in form of `{data}` packet.

**Example packet:**
```
{
  "data": {
    "topic": "usroVhYlDImeqk",
    "from": "usroVhYlDImeqk",
    "ts": "2017-08-09T00:44:47.484Z",
    "seq": 29,
    "content": "hello"
  }
}
```

**References:**

- https://github.com/tinode/chat/blob/devel/API.md#data

### Leave P2P Topic

After we're done with `p2p` topic, we should leave the topic to stop receiving message with target user.

**Example request:**
```
{
  "leave": {
    "id": "leave p2p connection with new2",
    "topic": "usroVhYlDImeqk"
  }
}
```
**Success response:**
```
{
  "ctrl": {
    "id": "leave p2p connection with new2",
    "topic": "usroVhYlDImeqk",
    "code": 200,
    "text": "ok",
    "ts": "2017-08-06T12:51:48.977Z"
  }
}
```
**Error responses:**
```
{
  "ctrl": {
    "id": "leave p2p connection with new2",
    "topic": "usroVhYlDImeqk",
    "code": 304,
    "text": "not joined",
    "ts": "2017-08-06T12:53:39.974Z"
  }
}
```
```
{
  "ctrl": {
    "id": "leave p2p connection with new2",
    "topic": "usroVhYlDImqk",
    "code": 400,
    "text": "malformed",
    "ts": "2017-08-06T12:54:21.598Z"
  }
}
```

**References:**

- https://github.com/tinode/chat/blob/devel/API.md#leave

## Logout

By design, Tinode doesn't support user logout. The reason is maybe because what Tinode authenticate is not the user, but rather the websocket session which uses user's credentials information. Thus if we want to achieve logout functionality in app, we just need to destroy the websocket connection, then start all over again.

**References:**

- https://github.com/tinode/chat/blob/devel/API.md#users
