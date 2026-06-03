import Ember from 'ember';

function getCookie(name) {
  var cookies = document.cookie ? document.cookie.split('; ') : [];

  for (var i = 0; i < cookies.length; i++) {
    var parts = cookies[i].split('=');
    var key = decodeURIComponent(parts.shift());

    if (key === name) {
      return decodeURIComponent(parts.join('='));
    }
  }
}

function setCookie(name, value) {
  document.cookie = encodeURIComponent(name) + '=' + encodeURIComponent(value) + '; path=/';
}

export default Ember.Controller.extend({
  applicationController: Ember.inject.controller('application'),
  stats: Ember.computed.reads('applicationController'),
  config: Ember.computed.reads('applicationController.config'),

	cachedLogin: Ember.computed('login', {
    get() {
      return this.get('login') || getCookie('login');
    },
    set(key, value) {
      setCookie('login', value);
      this.set('model.login', value);
      return value;
    }
  })
});
