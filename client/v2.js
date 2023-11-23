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

async function pageReady() {
  video = document.getElementById('video');

  audioContext = new (window.AudioContext || window.webkitAudioContext)({sampleRate:24000});

  function playWavFile(arrayBuffer) {
    audioContext.decodeAudioData(arrayBuffer, function(buffer) {
      let source = audioContext.createBufferSource();
      source.buffer = buffer;
      source.channelCount = 1;
      source.connect(audioContext.destination);
      source.start();
    });
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
          playWavFile(base64ToArrayBuffer(data.data))
          break
        default:
          log('unknown type')
          break;
      }
    };
  }

  connect();
}

function log(error) {
  console.log(error);
}
