package api

// WeChat protocol types mapping the ilink bot API.

// BaseInfo is attached to every outgoing request.
type BaseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
	BotAgent       string `json:"bot_agent,omitempty"`
}

// CDNMedia is the CDN media reference.
type CDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
}

// ImageItem describes an image message item.
type ImageItem struct {
	Media      *CDNMedia `json:"media,omitempty"`
	ThumbMedia *CDNMedia `json:"thumb_media,omitempty"`
	AESKey     string    `json:"aeskey,omitempty"`
	URL        string    `json:"url,omitempty"`
	MidSize    int       `json:"mid_size,omitempty"`
}

// VoiceItem describes a voice message item.
type VoiceItem struct {
	Media         *CDNMedia `json:"media,omitempty"`
	EncodeType    int       `json:"encode_type,omitempty"`
	BitsPerSample int       `json:"bits_per_sample,omitempty"`
	SampleRate    int       `json:"sample_rate,omitempty"`
	Playtime      int       `json:"playtime,omitempty"`
	Text          string    `json:"text,omitempty"`
}

// FileItem describes a file message item.
type FileItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	MD5      string    `json:"md5,omitempty"`
	Len      string    `json:"len,omitempty"`
}

// VideoItem describes a video message item.
type VideoItem struct {
	Media      *CDNMedia `json:"media,omitempty"`
	VideoSize  int       `json:"video_size,omitempty"`
	PlayLength int       `json:"play_length,omitempty"`
	VideoMD5   string    `json:"video_md5,omitempty"`
	ThumbMedia *CDNMedia `json:"thumb_media,omitempty"`
}

// TextItem describes a text message item.
type TextItem struct {
	Text string `json:"text,omitempty"`
}

// RefMessage is a reference to another message.
type RefMessage struct {
	MessageItem *MessageItem `json:"message_item,omitempty"`
	Title       string       `json:"title,omitempty"`
}

// MessageItem is a single item within a message.
type MessageItem struct {
	Type         int        `json:"type,omitempty"`
	CreateTimeMs int64      `json:"create_time_ms,omitempty"`
	UpdateTimeMs int64      `json:"update_time_ms,omitempty"`
	IsCompleted  bool       `json:"is_completed,omitempty"`
	MsgID        string     `json:"msg_id,omitempty"`
	RefMsg       *RefMessage `json:"ref_msg,omitempty"`
	TextItem     *TextItem  `json:"text_item,omitempty"`
	ImageItem    *ImageItem `json:"image_item,omitempty"`
	VoiceItem    *VoiceItem `json:"voice_item,omitempty"`
	FileItem     *FileItem  `json:"file_item,omitempty"`
	VideoItem    *VideoItem `json:"video_item,omitempty"`
}

// WeixinMessage is the unified message type.
type WeixinMessage struct {
	Seq           int            `json:"seq,omitempty"`
	MessageID     int64          `json:"message_id,omitempty"`
	FromUserID    string         `json:"from_user_id,omitempty"`
	ToUserID      string         `json:"to_user_id,omitempty"`
	ClientID      string         `json:"client_id,omitempty"`
	CreateTimeMs  int64          `json:"create_time_ms,omitempty"`
	UpdateTimeMs  int64          `json:"update_time_ms,omitempty"`
	DeleteTimeMs  int64          `json:"delete_time_ms,omitempty"`
	SessionID     string         `json:"session_id,omitempty"`
	GroupID       string         `json:"group_id,omitempty"`
	MessageType   int            `json:"message_type,omitempty"`
	MessageState  int            `json:"message_state,omitempty"`
	ItemList      []*MessageItem `json:"item_list,omitempty"`
	ContextToken  string         `json:"context_token,omitempty"`
}

// Message type constants.
const (
	MessageTypeNone = 0
	MessageTypeUser = 1
	MessageTypeBot  = 2
)

// Message item type constants.
const (
	ItemTypeNone  = 0
	ItemTypeText  = 1
	ItemTypeImage = 2
	ItemTypeVoice = 3
	ItemTypeFile  = 4
	ItemTypeVideo = 5
)

// Message state constants.
const (
	MessageStateNew        = 0
	MessageStateGenerating = 1
	MessageStateFinish     = 2
)

// Upload media type constants.
const (
	UploadMediaTypeImage = 1
	UploadMediaTypeVideo = 2
	UploadMediaTypeFile  = 3
	UploadMediaTypeVoice = 4
)

// Typing status constants.
const (
	TypingStatusTyping  = 1
	TypingStatusCancel  = 2
)

// GetUpdatesReq is the request for long-polling updates.
type GetUpdatesReq struct {
	GetUpdatesBuf string    `json:"get_updates_buf,omitempty"`
	BaseInfo      *BaseInfo `json:"base_info,omitempty"`
}

// GetUpdatesResp is the response to getUpdates.
type GetUpdatesResp struct {
	Ret                 int               `json:"ret,omitempty"`
	Errcode             int               `json:"errcode,omitempty"`
	Errmsg              string            `json:"errmsg,omitempty"`
	Msgs                []*WeixinMessage  `json:"msgs,omitempty"`
	GetUpdatesBuf       string            `json:"get_updates_buf,omitempty"`
	LongpollingTimeoutMs int              `json:"longpolling_timeout_ms,omitempty"`
}

// SendMessageReq wraps a single WeixinMessage for sending.
type SendMessageReq struct {
	Msg      *WeixinMessage `json:"msg,omitempty"`
	BaseInfo *BaseInfo      `json:"base_info,omitempty"`
}

// GetUploadUrlReq is the request to get a CDN upload URL.
type GetUploadUrlReq struct {
	FileKey          string `json:"filekey,omitempty"`
	MediaType        int    `json:"media_type,omitempty"`
	ToUserID         string `json:"to_user_id,omitempty"`
	RawSize          int    `json:"rawsize,omitempty"`
	RawFileMD5       string `json:"rawfilemd5,omitempty"`
	FileSize         int    `json:"filesize,omitempty"`
	ThumbRawSize     int    `json:"thumb_rawsize,omitempty"`
	ThumbRawFileMD5  string `json:"thumb_rawfilemd5,omitempty"`
	ThumbFileSize    int    `json:"thumb_filesize,omitempty"`
	NoNeedThumb      bool   `json:"no_need_thumb,omitempty"`
	AESKey           string `json:"aeskey,omitempty"`
	BaseInfo         *BaseInfo `json:"base_info,omitempty"`
}

// GetUploadUrlResp is the response from getUploadUrl.
type GetUploadUrlResp struct {
	UploadParam      string `json:"upload_param,omitempty"`
	ThumbUploadParam string `json:"thumb_upload_param,omitempty"`
	UploadFullURL    string `json:"upload_full_url,omitempty"`
}

// SendTypingReq sends a typing indicator.
type SendTypingReq struct {
	ILinkUserID  string    `json:"ilink_user_id,omitempty"`
	TypingTicket string    `json:"typing_ticket,omitempty"`
	Status       int       `json:"status,omitempty"`
	BaseInfo     *BaseInfo `json:"base_info,omitempty"`
}

// GetConfigResp is the response from getConfig.
type GetConfigResp struct {
	Ret          int    `json:"ret,omitempty"`
	Errmsg       string `json:"errmsg,omitempty"`
	TypingTicket string `json:"typing_ticket,omitempty"`
}

// NotifyStartReq notifies the server of client start.
type NotifyStartReq struct {
	BaseInfo *BaseInfo `json:"base_info,omitempty"`
}

// NotifyStartResp is the response to notifyStart.
type NotifyStartResp struct {
	Ret    int    `json:"ret,omitempty"`
	Errmsg string `json:"errmsg,omitempty"`
}

// NotifyStopReq notifies the server of client stop.
type NotifyStopReq struct {
	BaseInfo *BaseInfo `json:"base_info,omitempty"`
}

// NotifyStopResp is the response to notifyStop.
type NotifyStopResp struct {
	Ret    int    `json:"ret,omitempty"`
	Errmsg string `json:"errmsg,omitempty"`
}
