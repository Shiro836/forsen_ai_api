// import { Application } from '@pixi/app';
// import { Live2DModel } from 'cubism4';
// const live2d = require('pixi-live2d-display/cubism4');

let serverConnection;
let audioContext;

document.documentElement.addEventListener('click', () => {
  // ensuring the AudioContext is resumed
  if (audioContext.state === 'suspended') {
    audioContext.resume();
  }
  // now we know that audioContext is not suspended, we can create it if it doesn't exist
  if(!audioContext) {
    audioContext = new (window.AudioContext || window.webkitAudioContext)();
  }
});

function base64ToArrayBuffer(base64) {
  var binaryString =  window.atob(base64);
  var len = binaryString.length;
  var bytes = new Uint8Array(len);
  for (var i = 0; i < len; i++) {
    bytes[i] = binaryString.charCodeAt(i);
  }
  return bytes.buffer;
}

PIXI.live2d.ZipLoader.zipReader = (data, url) => JSZip.loadAsync(data);

PIXI.live2d.ZipLoader.readText = (jsZip, path) => {
    const file = jsZip.file(path);

    if (!file) {
        throw new Error('Cannot find file: ' + path);
    }

    return file.async('text');
};

PIXI.live2d.ZipLoader.getFilePaths = (jsZip) => {
    const paths = [];

    jsZip.forEach(relativePath => paths.push(relativePath));

    return Promise.resolve(paths);
};

PIXI.live2d.ZipLoader.getFiles = (jsZip, paths) =>
    Promise.all(paths.map(
        async path => {
            const fileName = path.slice(path.lastIndexOf('/') + 1);

            const blob = await jsZip.file(path).async('blob');

            return new File([blob], fileName);
        }));

PIXI.live2d.SoundManager.volume = 0.0;

async function pageReady() {
  const canvas = document.getElementById('canvas')

  const app = new PIXI.Application({
      view: canvas,
      autoResize: true,
      width: 800,
      height: 600,
      backgroundColor: '#0000ff'
  });

  model_proxy = null
  model_url = null

  function remove_model() {
    app.stage.removeChildren()

    model_proxy.destroy()

    model_proxy = null
    model_url = null
  }

  async function set_model_on_stage_url(url) {
    if (model_url === url) {
      return
    }
    app.stage.removeChildren()
    
    const model = await PIXI.live2d.Live2DModel.from('zip://' + url);
    model_proxy = model
    model_url = url
    log(model)

    model.x = 400;
    model.y = 550;
    model.anchor.set(0.5, 0.5);
    // model.position.y = (canvas.height / 2) - (model.height / 2);
  
    model.scale.set(0.25);
    // model.x = 150;
  
    app.stage.addChild(model);
  }

  function model_motion(category, index) {
    if (model_proxy != null) {
      model_proxy.motion(category, index, 2)
    }
  }

  async function play_model_audio(url) {
    if (model_proxy != null) {
      model_proxy.speak(url)
    }
  }

  audioContext = new (window.AudioContext || window.webkitAudioContext)({sampleRate:24000});

  const audio_sources = new Map();

  function playWavFile(arrayBuffer, msg_id) {
    audioContext.decodeAudioData(arrayBuffer, function(buffer) {
      let source = audioContext.createBufferSource();
      source.buffer = buffer;
      source.channelCount = 1;
      source.connect(audioContext.destination);
      source.start();
      
      audio_sources.set(msg_id, source)
    });
  }

  function skip(msg_id) {
    if (audio_sources.has(msg_id)) {
      console.log(audio_sources);
      console.log(audio_sources.get(msg_id));
      audio_sources.get(msg_id).stop();
    }
  }

  function connect() {
    ws = new WebSocket(`wss://${window.location.host + "/ws" + window.location.pathname}`);
    ws.binaryType = 'arraybuffer'
  
    ws.onerror = function(err) {
      console.error('Socket encountered error: ', err.message, 'Closing socket');
      ws.close();
    };
  
    ws.onclose = function(e) {
      console.log('Socket is closed. Reconnect will be attempted in 1 second.', e.reason);
      setTimeout(function() {
        connect();
      }, 1000);
    };

    const models = new Map();

    function set_image(url) {
      document.getElementById("char_image").src=url;
    }

    function set_model(char_name) {
      if (char_name === '') {
        remove_model()
        return
      }
      set_model_on_stage_url('/get_model/'+char_name)
      return

      if (models.has(char_name)) {
        log(char_name + ' is in cache')
        set_model_on_stage(models.get(char_name))
        return
      }

      const xhr = new XMLHttpRequest();
      xhr.open("GET", "/get_model/" + char_name, true);
      xhr.onload = (e) => {
        if (xhr.readyState === 4) {
          if (xhr.status === 200) {
            model = base64ToArrayBuffer(xhr.response);
            models.set(char_name, model);
            log('cached ' + char_name)
            set_model_on_stage(model)
          } else {
            log('get_model err - ' + xhr.statusText);
          }
        }
      };
      xhr.onerror = (e) => {
        console.error(xhr.statusText);
      };
      xhr.send(null);
    }

    ws.onmessage = function (event) {
      log(event.data)

      let uint8Array = new Uint8Array(event.data);

      let decoder = new TextDecoder('utf-8');
      let utf8String = decoder.decode(uint8Array);

      data = JSON.parse(utf8String)

      log(data)
      switch (data.type) {
        case 'text':
          document.getElementById("text").innerText=data.data;
          break
        case 'audio':
          let json = JSON.parse(data.data) 

          // model_motion("00_Anger_01", 0)
          rawAudio = base64ToArrayBuffer(json['audio'])
          // var rawAudioCopy = new ArrayBuffer(rawAudio.byteLength);
          // new Uint8Array(rawAudioCopy).set(new Uint8Array(rawAudio));

          if (model_proxy === null) {
            playWavFile(rawAudio, json['msg_id'])
            break
          }

          const dataArray = new Uint8Array(rawAudio);

          const blob = new Blob([dataArray], { type: 'audio/wav' });

          const blobUrl = URL.createObjectURL(blob);
          play_model_audio(blobUrl)
          // PIXI.live2d.SoundManager.play(new Audio(blobUrl))
          break
        case 'model':
          set_model(data.data)
          break
        case 'image':
          set_image(data.data)
          break
        case 'skip':
          skip(data.data)
          break
        case 'ping':
          break
        default:
          log('unknown type')
          break;
      }
    };
  }

  connect();
  // set_model_on_stage_url('/get_model/'+"megumin")
  // 00_Anger_01
  // setTimeout(function() { model_motion("", 20); }, 1000);
}

function log(error) {
  console.log(error);
}
