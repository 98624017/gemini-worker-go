协同统一修改此两个子目录项目/async-gateway和/rust-sync-proxy,原两项目都主要是针对Gemini的Banana相关,现需要相同的针对gpt-image-2设计一套Newapi的前置异步接口入口和一套newapi对接真实上游的改写rust层.
客户端异步提交任务获得任务id,除请求端点和请求体和返回体跟gemini的不一致外 其余都复用原机制.
Post (async-gateway的baseurl)/v1/images/generations
Authorization: Bearer <API_KEY>   (用户的Newapi的apikey) 
{
  "model": "gpt-image-2",
  "prompt": "创建图片,4K,一张淘宝购物页面的手机截图,图片中的女模特拿着图片中的拖鞋,",
  "image": [
    "https://d.uguu.se/vJJYVqCx.jpg",
    "https://n.uguu.se/ZNpRxTUK.jpg"
  ],
  "response_format": "url"
}

经由异步服务层转发,async-gateway同步请求Newapi层处理计费事宜转至/rust-sync-proxy这个中间改写层
Post (rust-sync-proxy的baseurl)/v1/images/generations
Authorization: Bearer <baseurl|API_KEY>   (最终真实上游baseurl|最终真实上游的apikey) 
{
  "model": "gpt-image-2",
  "prompt": "创建图片,4K,一张淘宝购物页面的手机截图,图片中的女模特拿着图片中的拖鞋,",
  "image": [
    "https://d.uguu.se/vJJYVqCx.jpg",
    "https://n.uguu.se/ZNpRxTUK.jpg"
  ],
  "response_format": "url"
}
改写为
Post baseurl/v1/images/generations
Authorization: Bearer <API_KEY>   (最终真实上游的apikey) 

{
  "model": "gpt-image-2",
  "prompt": "创建图片,4K,一张淘宝购物页面的手机截图,图片中的女模特拿着图片中的拖鞋,",
  "reference_images": [
    "https://d.uguu.se/vJJYVqCx.jpg",
    "https://n.uguu.se/ZNpRxTUK.jpg"
  ],
  "response_format": "b64_json"
}


返回体改写,转存图床or r2,另外固定构建个useage让newapi更稳健触发计费

 {
    "created": 1776663103,
    "data": [
      {
        "url": "https://..."     //复用项目已有的上传图床或r2的机制
      }
    ],
    "usage": {
      "input_tokens": 1024,
      "input_tokens_details": {
        "image_tokens": 1000,
        "text_tokens": 24
      },
      "output_tokens": 1024,
      "total_tokens": 2048,
      "output_tokens_details": {
        "image_tokens": 1024,
        "text_tokens": 0
      }
    }
  }
  }

