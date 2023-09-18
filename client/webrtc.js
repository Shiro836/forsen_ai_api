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

function PCMPlayer(option) {
  this.init(option);
}

PCMPlayer.prototype.init = function(option) {
  var defaults = {
      encoding: '16bitInt',
      channels: 1,
      sampleRate: 8000,
      flushingTime: 1000
  };
  this.option = Object.assign({}, defaults, option);
  this.samples = new Float32Array();
  this.flush = this.flush.bind(this);
  this.interval = setInterval(this.flush, this.option.flushingTime);
  this.maxValue = this.getMaxValue();
  this.typedArray = this.getTypedArray();
  this.createContext();
};

PCMPlayer.prototype.getMaxValue = function () {
  var encodings = {
      '8bitInt': 128,
      '16bitInt': 32768,
      '32bitInt': 2147483648,
      '32bitFloat': 1
  }

  return encodings[this.option.encoding] ? encodings[this.option.encoding] : encodings['16bitInt'];
};

PCMPlayer.prototype.getTypedArray = function () {
  var typedArrays = {
      '8bitInt': Int8Array,
      '16bitInt': Int16Array,
      '32bitInt': Int32Array,
      '32bitFloat': Float32Array
  }

  return typedArrays[this.option.encoding] ? typedArrays[this.option.encoding] : typedArrays['16bitInt'];
};

PCMPlayer.prototype.createContext = function() {
  this.audioCtx = new (window.AudioContext || window.webkitAudioContext)();

  // context needs to be resumed on iOS and Safari (or it will stay in "suspended" state)
  this.audioCtx.resume();
  this.audioCtx.onstatechange = () => console.log(this.audioCtx.state);   // if you want to see "Running" state in console and be happy about it
  
  this.gainNode = this.audioCtx.createGain();
  this.gainNode.gain.value = 1;
  this.gainNode.connect(this.audioCtx.destination);
  this.startTime = this.audioCtx.currentTime;
};

PCMPlayer.prototype.isTypedArray = function(data) {
  return (data.byteLength && data.buffer && data.buffer.constructor == ArrayBuffer);
};

PCMPlayer.prototype.feed = function(data) {
  if (!this.isTypedArray(data)) return;
  data = this.getFormatedValue(data);
  var tmp = new Float32Array(this.samples.length + data.length);
  tmp.set(this.samples, 0);
  tmp.set(data, this.samples.length);
  this.samples = tmp;
};

PCMPlayer.prototype.isAudioPlaying = function() {
  if (!this.audioCtx) {
    return false;
  }
  var analyser = this.audioCtx.createAnalyser();
  var bufferLength = analyser.fftSize;
  var dataArray = new Float32Array(bufferLength);

  analyser.getFloatTimeDomainData(dataArray);
  for (var i = 0; i < bufferLength; i++) {
    if (dataArray[i] != 0) return true;
  }
  return false;
}

PCMPlayer.prototype.getFormatedValue = function(data) {
  var data = new this.typedArray(data.buffer),
      float32 = new Float32Array(data.length),
      i;

  for (i = 0; i < data.length; i++) {
      float32[i] = data[i] / this.maxValue;
  }
  return float32;
};

PCMPlayer.prototype.volume = function(volume) {
  this.gainNode.gain.value = volume;
};

PCMPlayer.prototype.destroy = function() {
  if (this.interval) {
      clearInterval(this.interval);
  }
  this.samples = null;
  this.audioCtx.close();
  this.audioCtx = null;
};

var onEnd;

PCMPlayer.prototype.flush = function() {
  if (!this.samples.length) return;
  var bufferSource = this.audioCtx.createBufferSource(),
      length = this.samples.length / this.option.channels,
      audioBuffer = this.audioCtx.createBuffer(this.option.channels, length, this.option.sampleRate),
      audioData,
      channel,
      offset,
      i,
      decrement;

  for (channel = 0; channel < this.option.channels; channel++) {
      audioData = audioBuffer.getChannelData(channel);
      offset = channel;
      decrement = 50;
      for (i = 0; i < length; i++) {
          audioData[i] = this.samples[offset];
          /* fadein */
          if (i < 50) {
              audioData[i] =  (audioData[i] * i) / 50;
          }
          /* fadeout*/
          if (i >= (length - 51)) {
              audioData[i] =  (audioData[i] * decrement--) / 50;
          }
          offset += this.option.channels;
      }
  }
  
  if (this.startTime < this.audioCtx.currentTime) {
      this.startTime = this.audioCtx.currentTime;
  }
  console.log('start vs current '+this.startTime+' vs '+this.audioCtx.currentTime+' duration: '+audioBuffer.duration);
  bufferSource.buffer = audioBuffer;
  bufferSource.onended = onEnd;
  bufferSource.connect(this.gainNode);
  bufferSource.start(this.startTime);
  this.startTime += audioBuffer.duration;
  this.samples = new Float32Array();
};


async function pageReady() {
  video = document.getElementById('video');

  audioContext = new (window.AudioContext || window.webkitAudioContext)();

  var player = new PCMPlayer({
    encoding: '16bitFloat',
    channels: 1,
    sampleRate: 22050,
    flushingTime: 300
  });

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
      if (typeof event.data === "string") {
        if (event.data === "heartbeat") {
          ws.send("heartbeat")
        } else {
          document.getElementById("text").innerHTML=event.data;
        }
  
        return
      } else {
        onEnd = function() {
          ws.send("finish")
        }
    
        player.feed(new Int16Array(event.data));
      }
    };
  }

  connect();
}

function playSound(buffer) {
  var source = audioContext.createBufferSource();
  source.buffer = buffer;
  source.connect(audioContext.destination);
  source.start();
}

function log(error) {
  console.log(error);
}
